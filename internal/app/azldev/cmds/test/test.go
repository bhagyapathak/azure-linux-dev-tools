// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package test

import (
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/spf13/cobra"
)

func OnAppInit(app *azldev.App) {
	cmd := NewTestCmd()
	app.AddTopLevelCommand(cmd)
}

// NewTestCmd constructs a [cobra.Command] for the 'test' command.
func NewTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test workflow and orchestration commands",
		Long: `Test workflow and orchestration commands for Azure Linux development.

This command group provides tools for defining test workflows, managing
test configurations, and integrating with testing frameworks.`,
	}

	// Add subcommands
	cmd.AddCommand(NewDefineWorkflowCmd())

	return cmd
}