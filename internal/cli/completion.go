package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newCompletionCmd returns the shell completion command.
// It generates completion scripts for bash, zsh, fish, and powershell.
func (c *CLI) newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for Petri.

To load completions:

Bash:
  $ source <(petri completion bash)
  # To load completions for each session, add to ~/.bashrc:
  $ petri completion bash > ~/.bash_completion

Zsh:
  # If shell completion is not already enabled, execute:
  $ echo "autoload -U compinit; compinit" >> ~/.zshrc
  # To load completions for each session:
  $ petri completion zsh > "${fpath[1]}/_petri"

Fish:
  $ petri completion fish | source
  # To load completions for each session:
  $ petri completion fish > ~/.config/fish/completions/petri.fish

PowerShell:
  PS> petri completion powershell | Out-String | Invoke-Expression
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(os.Stdout)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unknown shell %q; choose one of: bash, zsh, fish, powershell", args[0])
			}
		},
	}
	return cmd
}
