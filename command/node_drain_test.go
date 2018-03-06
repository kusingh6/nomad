package command

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/command/agent"
	"github.com/hashicorp/nomad/helper"
	"github.com/hashicorp/nomad/testutil"
	"github.com/mitchellh/cli"
	"github.com/posener/complete"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeDrainCommand_Implements(t *testing.T) {
	t.Parallel()
	var _ cli.Command = &NodeDrainCommand{}
}

func TestNodeDrainCommand_Detach(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	server, client, url := testServer(t, true, func(c *agent.Config) {
		c.NodeName = "drain_detach_node"
	})
	defer server.Shutdown()

	// Wait for a node to appear
	var nodeID string
	testutil.WaitForResult(func() (bool, error) {
		nodes, _, err := client.Nodes().List(nil)
		if err != nil {
			return false, err
		}
		if len(nodes) == 0 {
			return false, fmt.Errorf("missing node")
		}
		nodeID = nodes[0].ID
		return true, nil
	}, func(err error) {
		t.Fatalf("err: %s", err)
	})

	// Register a job to create an alloc to drain that will block draining
	job := &api.Job{
		ID:          helper.StringToPtr("mock_service"),
		Name:        helper.StringToPtr("mock_service"),
		Datacenters: []string{"dc1"},
		TaskGroups: []*api.TaskGroup{
			{
				Name: helper.StringToPtr("mock_group"),
				Tasks: []*api.Task{
					{
						Name:   "mock_task",
						Driver: "mock_driver",
						Config: map[string]interface{}{
							"run_for":    "10m",
							"exit_after": "10m",
							"kill_after": "10m",
						},
					},
				},
			},
		},
	}

	_, _, err := client.Jobs().Register(job, nil)
	require.Nil(err)

	testutil.WaitForResult(func() (bool, error) {
		allocs, _, err := client.Nodes().Allocations(nodeID, nil)
		if err != nil {
			return false, err
		}
		if len(allocs) == 0 {
			return false, fmt.Errorf("no allocs")
		}
		return true, nil
	}, func(err error) {
		t.Fatalf("err: %v", err)
	})

	ui := new(cli.MockUi)
	cmd := &NodeDrainCommand{Meta: Meta{Ui: ui}}
	if code := cmd.Run([]string{"-address=" + url, "-self", "-enable", "-detach"}); code != 0 {
		t.Fatalf("expected exit 0, got: %d", code)
	}

	out := ui.OutputWriter.String()
	expected := "drain strategy set"
	require.Contains(out, expected)

	node, _, err := client.Nodes().Info(nodeID, nil)
	require.Nil(err)
	require.NotNil(node.DrainStrategy)
}

func TestNodeDrainCommand_Monitor(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	server, client, url := testServer(t, true, func(c *agent.Config) {
		c.NodeName = "drain_monitor_node"
	})
	defer server.Shutdown()

	// Wait for a node to appear
	var nodeID string
	testutil.WaitForResult(func() (bool, error) {
		nodes, _, err := client.Nodes().List(nil)
		if err != nil {
			return false, err
		}
		if len(nodes) == 0 {
			return false, fmt.Errorf("missing node")
		}
		nodeID = nodes[0].ID
		return true, nil
	}, func(err error) {
		t.Fatalf("err: %s", err)
	})

	// Register a job to create an alloc to drain
	job := &api.Job{
		ID:          helper.StringToPtr("mock_service"),
		Name:        helper.StringToPtr("mock_service"),
		Datacenters: []string{"dc1"},
		TaskGroups: []*api.TaskGroup{
			{
				Name: helper.StringToPtr("mock_group"),
				Tasks: []*api.Task{
					{
						Name:   "mock_task",
						Driver: "mock_driver",
						Config: map[string]interface{}{
							"run_for":    "10m",
							"exit_after": "500ms",
							"kill_after": "10m",
						},
					},
				},
			},
		},
	}

	_, _, err := client.Jobs().Register(job, nil)
	require.Nil(err)

	testutil.WaitForResult(func() (bool, error) {
		allocs, _, err := client.Nodes().Allocations(nodeID, nil)
		if err != nil {
			return false, err
		}
		if len(allocs) == 0 {
			return false, fmt.Errorf("no allocs")
		}
		return true, nil
	}, func(err error) {
		t.Fatalf("err: %v", err)
	})

	ui := new(cli.MockUi)
	cmd := &NodeDrainCommand{Meta: Meta{Ui: ui}}
	if code := cmd.Run([]string{"-address=" + url, "-self", "-enable", "-deadline", "1s"}); code != 0 {
		t.Fatalf("expected exit 0, got: %d", code)
	}

	out := ui.OutputWriter.String()
	expected := "drain complete"
	require.Contains(out, expected)

	node, _, err := client.Nodes().Info(nodeID, nil)
	require.Nil(err)
	require.Nil(node.DrainStrategy)

	allocs, _, err := client.Nodes().Allocations(nodeID, nil)
	require.Nil(err)
	require.Len(allocs, 1)
	require.Equal("stop", allocs[0].DesiredStatus)
}

func TestNodeDrainCommand_Fails(t *testing.T) {
	t.Parallel()
	srv, _, url := testServer(t, false, nil)
	defer srv.Shutdown()

	ui := new(cli.MockUi)
	cmd := &NodeDrainCommand{Meta: Meta{Ui: ui}}

	// Fails on misuse
	if code := cmd.Run([]string{"some", "bad", "args"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, cmd.Help()) {
		t.Fatalf("expected help output, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fails on connection failure
	if code := cmd.Run([]string{"-address=nope", "-enable", "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
		t.Fatalf("expected exit code 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "Error toggling") {
		t.Fatalf("expected failed toggle error, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fails on non-existent node
	if code := cmd.Run([]string{"-address=" + url, "-enable", "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "No node(s) with prefix or id") {
		t.Fatalf("expected not exist error, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fails if both enable and disable specified
	if code := cmd.Run([]string{"-enable", "-disable", "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, cmd.Help()) {
		t.Fatalf("expected help output, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fails if neither enable or disable specified
	if code := cmd.Run([]string{"12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, cmd.Help()) {
		t.Fatalf("expected help output, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fail on identifier with too few characters
	if code := cmd.Run([]string{"-address=" + url, "-enable", "1"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "must contain at least two characters.") {
		t.Fatalf("expected too few characters error, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Identifiers with uneven length should produce a query result
	if code := cmd.Run([]string{"-address=" + url, "-enable", "123"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "No node(s) with prefix or id") {
		t.Fatalf("expected not exist error, got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fail on disable being used with drain strategy flags
	for _, flag := range []string{"-force", "-no-deadline", "-ignore-system"} {
		if code := cmd.Run([]string{"-address=" + url, "-disable", flag, "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
			t.Fatalf("expected exit 1, got: %d", code)
		}
		if out := ui.ErrorWriter.String(); !strings.Contains(out, "combined with flags configuring drain strategy") {
			t.Fatalf("got: %s", out)
		}
		ui.ErrorWriter.Reset()
	}

	// Fail on setting a deadline plus deadline modifying flags
	for _, flag := range []string{"-force", "-no-deadline"} {
		if code := cmd.Run([]string{"-address=" + url, "-enable", "-deadline=10s", flag, "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
			t.Fatalf("expected exit 1, got: %d", code)
		}
		if out := ui.ErrorWriter.String(); !strings.Contains(out, "deadline can't be combined with") {
			t.Fatalf("got: %s", out)
		}
		ui.ErrorWriter.Reset()
	}

	// Fail on setting a force and no deadline
	if code := cmd.Run([]string{"-address=" + url, "-enable", "-force", "-no-deadline", "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
		t.Fatalf("expected exit 1, got: %d", code)
	}
	if out := ui.ErrorWriter.String(); !strings.Contains(out, "mutually exclusive") {
		t.Fatalf("got: %s", out)
	}
	ui.ErrorWriter.Reset()

	// Fail on setting a bad deadline
	for _, flag := range []string{"-deadline=0s", "-deadline=-1s"} {
		if code := cmd.Run([]string{"-address=" + url, "-enable", flag, "12345678-abcd-efab-cdef-123456789abc"}); code != 1 {
			t.Fatalf("expected exit 1, got: %d", code)
		}
		if out := ui.ErrorWriter.String(); !strings.Contains(out, "positive") {
			t.Fatalf("got: %s", out)
		}
		ui.ErrorWriter.Reset()
	}
}

func TestNodeDrainCommand_AutocompleteArgs(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()

	srv, client, url := testServer(t, true, nil)
	defer srv.Shutdown()

	// Wait for a node to appear
	var nodeID string
	testutil.WaitForResult(func() (bool, error) {
		nodes, _, err := client.Nodes().List(nil)
		if err != nil {
			return false, err
		}
		if len(nodes) == 0 {
			return false, fmt.Errorf("missing node")
		}
		nodeID = nodes[0].ID
		return true, nil
	}, func(err error) {
		t.Fatalf("err: %s", err)
	})

	ui := new(cli.MockUi)
	cmd := &NodeDrainCommand{Meta: Meta{Ui: ui, flagAddress: url}}

	prefix := nodeID[:len(nodeID)-5]
	args := complete.Args{Last: prefix}
	predictor := cmd.AutocompleteArgs()

	res := predictor.Predict(args)
	assert.Equal(1, len(res))
	assert.Equal(nodeID, res[0])
}
