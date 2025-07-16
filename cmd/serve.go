/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ikasoba/zettelo/core"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Starts as an MCP server.",
	Long:  `Starts as an MCP server.`,
	Run:   runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	defaultHome := os.Getenv("ZETTELO_HOME")
	if len(defaultHome) <= 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}

		defaultHome = filepath.Join(home, ".zettelo")
	}

	serveCmd.Flags().String("home", defaultHome, "Secify note directory.")

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// serveCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// serveCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

func runServe(cmd *cobra.Command, args []string) {
	flags := cmd.Flags()
	home, err := flags.GetString("home")
	if err != nil {
		panic(err)
	}

	notes, err := core.New(home)
	if err != nil {
		panic(err)
	}

	z := ZetteloMCP{
		notes: notes,
	}
	defer z.notes.Close()

	s := server.NewMCPServer(
		"Zettelo",
		"0.1.0",
		server.WithToolCapabilities(false),
	)

	{
		tool := mcp.NewTool("read_note",
			mcp.WithDescription("Retrieve notes from the Zettelkasten-style knowledge database."),
			mcp.WithString("name",
				mcp.Required(),
				mcp.Description("Name of zettelkasten note."),
			),
		)

		s.AddTool(tool, z.readHandler)
	}

	{
		tool := mcp.NewTool("put_note",
			mcp.WithDescription(`Save the note in Zettelkasten format in the Knowledge Database.
The name is unique to each Knowledge, and executing this action on an existing note will overwrite it with the new content.

The following properties can be specified for frontmatter.

- `+"`tags`"+`: 
 An array of strings, representing the tags associated with the note.　Must be set.

- `+"`created_at`"+`: 
 The time the note was created.

- `+"`updated_at`"+`: 
 The time the note was updated.`),
			mcp.WithString("name",
				mcp.Required(),
				mcp.Description("Name of zettelkasten note."),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("Contents to be stored in the knowledge database")),
		)

		s.AddTool(tool, z.putHandler)
	}

	{
		tool := mcp.NewTool("search_notes_by_tags",
			mcp.WithDescription(`Searches for tagged notes that exist in the Zettelkasten Expression knowledge database.

In query, you can use the `+"`|`"+` operator for OR conditions and the `+"`&`"+` operator for AND conditions.

The precedence of operators is as follows. Note that `+"`AND > OR`"+`, parentheses, etc. cannot be used.
And the query must contain at least one tag.`),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Name of zettelkasten note."),
			),
			mcp.WithString("last_seek_position",
				mcp.Description(`Seek cursor to specified position when searching.
This allows you to continue searching from previous results by entering the JSON returned from a previous search if you want to search more notes.`)),
		)

		s.AddTool(tool, z.searchNoteByTagsHandler)
	}

	{
		tool := mcp.NewTool("get_tags_stats",
			mcp.WithDescription("Lists tags and the number of notes associated with those tags."),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of items to list."),
				mcp.DefaultNumber(0),
			),
			mcp.WithString("last_seek_position",
				mcp.Description("Seek position of previous search results, to continue from the full open search results you must enter the string `last_seek_position` returned from the previous output."),
			),
		)

		s.AddTool(tool, z.getTagsStatsHandler)
	}

	if err := server.ServeStdio(s); err != nil {
		fmt.Errorf("Server error: %v\n", err)
	}
}

type ZetteloMCP struct {
	notes *core.Zettelo
}

func (z *ZetteloMCP) readHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")

	var buf bytes.Buffer

	if err := z.notes.ReadNote(name, &buf); err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	return mcp.NewToolResultText(buf.String()), nil
}

func (z *ZetteloMCP) putHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("name", "")
	content := req.GetString("content", "")

	var buf bytes.Buffer

	if _, err := buf.WriteString(content); err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	if err := z.notes.PutNote(name, &buf); err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	return mcp.NewToolResultText("Note has been saved."), nil
}

func (z *ZetteloMCP) searchNoteByTagsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rawQuery := req.GetString("query", "")
	rawLastSeekPosition := req.GetString("last_seek_position", "{}")

	query := [][]string{}

	for _, v := range strings.Split(rawQuery, "|") {
		rawAndQuery := strings.Split(v, "&")
		trimedAndQuery := []string{}

		for _, q := range rawAndQuery {

			q = (regexp.MustCompile(`\s+`).ReplaceAllString(q, ""))

			if len(q) > 0 {
				trimedAndQuery = append(trimedAndQuery, q)
			}
		}

		if len(trimedAndQuery) > 0 {
			query = append(query, trimedAndQuery)
		}
	}

	var seeks map[string]string
	if err := json.Unmarshal([]byte(rawLastSeekPosition), &seeks); err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	notes, lastSeekPosition, err := z.notes.Filter(query, seeks, 100)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	byteLastSeekPosition, err := json.Marshal(lastSeekPosition)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	result := fmt.Sprintf(
		"# last seek position\n```json\n%s\n```\n\n# notes (results: %d)'\n", string(byteLastSeekPosition), len(notes),
	)

	for _, name := range notes {
		result += fmt.Sprintf("- `%s`\n", name)
	}

	return mcp.NewToolResultText(result), nil
}
func (z *ZetteloMCP) getTagsStatsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := req.GetInt("limit", 100)
	lastSeekPosition := req.GetString("last_seek_position", "")

	stats, lastSeekPosition, err := z.notes.GetTagsStats(lastSeekPosition, limit)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), err
	}

	result := fmt.Sprintf(
		"# last seek position\n```plain\n%s\n```\n\n# stats (limit: %d) (results: %d)'\n", lastSeekPosition, limit, len(stats),
	)

	for name, count := range stats {
		result += fmt.Sprintf("- `%s`: %d\n", name, count)
	}

	return mcp.NewToolResultText(result), nil
}
