package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zach-snell/bbkt/internal/bitbucket"
)

var downloadsCmd = &cobra.Command{
	Use:     "downloads",
	Aliases: []string{"dl"},
	GroupID: groupData,
	Short:   "List and fetch files in a repository's Downloads section",
	Long: `List and fetch files in a repository's Downloads section (build artifacts
uploaded by pipelines, release packages, ...).
Workspace/repo are inferred from your git clone when omitted.

Alias: dl`,
	Example: `  bbkt downloads list
  bbkt downloads get app_release_1a2b3c4.zip
  bbkt downloads get app_release_1a2b3c4.zip -o artifacts/app.zip`,
}

var downloadsListCmd = &cobra.Command{
	Use:   "list [workspace] [repo-slug]",
	Short: "List Downloads, newest first",
	Args:  cobra.RangeArgs(0, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, repoSlug, _, err := ParseArgs(cmd, args, 0)
		if err != nil {
			return err
		}
		page, pagelen := paginationArgs(cmd)

		result, err := getClient().ListDownloads(bitbucket.ListDownloadsArgs{
			Workspace: workspace,
			RepoSlug:  repoSlug,
			Page:      page,
			Pagelen:   pagelen,
		})
		if err != nil {
			return err
		}

		PrintOrJSON(cmd, result, func() {
			if len(result.Values) == 0 {
				fmt.Println("No downloads found.")
				return
			}
			t := NewTable()
			t.Header("Name", "Size", "Downloads", "Uploaded", "By")
			for _, d := range result.Values {
				by := "-"
				if d.User != nil && d.User.DisplayName != "" {
					by = d.User.DisplayName
				}
				t.Row(d.Name, formatBytes(d.Size), fmt.Sprintf("%d", d.Downloads), FormatTime(d.CreatedOn), by)
			}
			t.Flush()
			PrintPaginationFooter(result.Size, result.Page, len(result.Values), result.Next != "")
		})
		return nil
	},
}

var downloadsGetCmd = &cobra.Command{
	Use:   "get [workspace] [repo-slug] <file-name>",
	Short: "Download a file from Downloads (to ./<file-name> unless -o is given)",
	Args:  cobra.RangeArgs(1, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, repoSlug, trailing, err := ParseArgs(cmd, args, 1)
		if err != nil {
			return err
		}
		name := trailing[0]
		out, _ := cmd.Flags().GetString("output")
		force, _ := cmd.Flags().GetBool("force")
		if out == "" {
			out = filepath.Base(name)
		}
		if _, err := os.Stat(out); err == nil && !force {
			return fmt.Errorf("%s already exists (use --force to overwrite)", out)
		}

		// Write to a temp file beside the target so a failed download never
		// leaves a partial file under the final name.
		tmp, err := os.CreateTemp(filepath.Dir(out), ".bbkt-download-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name()) // no-op after a successful rename

		n, err := getClient().GetDownload(bitbucket.GetDownloadArgs{Workspace: workspace, RepoSlug: repoSlug, Name: name}, tmp)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if force {
			_ = os.Remove(out) // Windows rename does not replace an existing file
		}
		if err := os.Rename(tmp.Name(), out); err != nil {
			return err
		}

		PrintOrJSON(cmd, map[string]any{"name": name, "path": out, "bytes": n}, func() {
			fmt.Printf("Saved %s (%s) to %s\n", name, formatBytes(n), out)
		})
		return nil
	},
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func init() {
	RootCmd.AddCommand(downloadsCmd)
	downloadsCmd.AddCommand(downloadsListCmd)
	downloadsCmd.AddCommand(downloadsGetCmd)
	addPaginationFlags(downloadsListCmd)
	downloadsGetCmd.Flags().StringP("output", "o", "", "Destination path (default ./<file-name>)")
	downloadsGetCmd.Flags().Bool("force", false, "Overwrite the destination if it exists")
}
