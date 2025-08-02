package cmd

import (
	"github.com/sensepost/gowitness/internal/ascii"
	"github.com/sensepost/gowitness/web"
	"github.com/spf13/cobra"
)

var serverCmdTopLevelFlags = struct {
	Host           string
	Port           int
	DbUri          string
	ScreenshotPath string
}{}

var serverCmdTopLevel = &cobra.Command{
	Use:   "server",
	Short: "Start the web user interface",
	Long: ascii.LogoHelp(ascii.Markdown(`
# server

Start the web user interface. This is a shortcut for 'gowitness report server'.`)),
	Example: ascii.Markdown(`
- gowitness server
- gowitness server --port 8080 --db-uri /tmp/gowitness.sqlite3
- gowitness server --screenshot-path /tmp/screenshots`),
	Run: func(cmd *cobra.Command, args []string) {
		server := web.NewServer(
			serverCmdTopLevelFlags.Host,
			serverCmdTopLevelFlags.Port,
			serverCmdTopLevelFlags.DbUri,
			serverCmdTopLevelFlags.ScreenshotPath,
		)
		server.Run()
	},
}

func init() {
	rootCmd.AddCommand(serverCmdTopLevel)

	serverCmdTopLevel.Flags().StringVar(&serverCmdTopLevelFlags.Host, "host", "127.0.0.1", "The host address to bind the webserver to")
	serverCmdTopLevel.Flags().IntVar(&serverCmdTopLevelFlags.Port, "port", 7171, "The port to start the web server on")
	serverCmdTopLevel.Flags().StringVar(&serverCmdTopLevelFlags.DbUri, "db-uri", "sqlite:///tmp/gowitness.sqlite3", "The database URI to use. Supports SQLite, Postgres, and MySQL (e.g., postgres://user:pass@host:port/db)")
	serverCmdTopLevel.Flags().StringVar(&serverCmdTopLevelFlags.ScreenshotPath, "screenshot-path", "/tmp/gowitness-screenshots", "The path where screenshots are stored")
}
