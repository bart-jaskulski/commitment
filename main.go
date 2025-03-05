package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/urfave/cli/v3"
)

//go:embed prompt
var systemPrompt string

const (
	maxTokens   = 120
	apiEndpoint = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	model       = "gemini-2.0-flash"
)

type OpenAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

var rootCmd = &cli.Command{
	Name:  "commitment",
	Usage: "Generate commit messages and install git hooks",
	Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.Args().Len() < 1 {
			return fmt.Errorf("Error: No commit message file provided")
		}

		commitMsgFile := cmd.Args().Get(0)
		commitType := ""
		if cmd.Args().Len() > 1 {
			commitType = cmd.Args().Get(1)
		}

		// Skip in these cases
		if shouldSkip(commitType, commitMsgFile) {
			fmt.Println("⚠️ Skipping commit message generation")
			return nil
		}

		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			fmt.Println("⚠️ GEMINI_API_KEY not set, skipping commit message generation")
			return nil
		}

		// Get diff and changed files
		diff := getGitDiff()
		if diff == "" {
			// No changes to commit
			return nil
		}

		changedFiles := getChangedFiles()

		// Generate message
		message := generateCommitMessage(diff, changedFiles, apiKey)
		if message != "" {
			updateCommitMessageFile(message, commitMsgFile)
		}

		return nil
	},
	Commands: []*cli.Command{
		{
			Name:  "install",
			Usage: "Install as a git commit hook",
			Aliases: []string{"i"},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				// Get the git repository root
				gitCmd := exec.Command("git", "rev-parse", "--git-dir")
				output, err := gitCmd.Output()
				if err != nil {
					return fmt.Errorf("Failed to get git directory: %w", err)
				}

				gitDir := strings.TrimSpace(string(output))
				hookPath := filepath.Join(gitDir, "hooks", "prepare-commit-msg")

				// Get the path to the current executable
				execPath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("Failed to get executable path: %w", err)
				}

				// Create the hooks directory if it doesn't exist
				hooksDir := filepath.Join(gitDir, "hooks")
				if err := os.MkdirAll(hooksDir, 0755); err != nil {
					return fmt.Errorf("Failed to create hooks directory: %w", err)
				}

				// Create the hook script
				hookContent := fmt.Sprintf(`#!/bin/sh
					# Commit message generator hook
					%s "$@"
					`, execPath)

				if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
					return fmt.Errorf("Failed to write hook file: %w", err)
				}

				fmt.Printf("✅ Commit hook installed at %s\n", hookPath)
				return nil
			},
		},
	},
}

func main() {
	if err := rootCmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

func shouldSkip(commitType, commitMsgFile string) bool {
	// Skip if commit type is anything other than an empty message
	if commitType != "" {
		return true
	}

	// Check if the message file already has content
	content, err := os.ReadFile(commitMsgFile)
	if err == nil {
		contentStr := strings.TrimSpace(string(content))
		if contentStr != "" && !strings.HasPrefix(contentStr, "#") {
			return true
		}
	}

	return false
}

func getGitDiff() string {
	cmd := exec.Command("git", "diff", "--staged")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	diff := string(output)
	return diff
}

func getChangedFiles() string {
	cmd := exec.Command("git", "diff", "--staged", "--name-status")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return string(output)
}

func getCurrentAuthorRecentCommits() string {
	// Get current author's email
	emailCmd := exec.Command("git", "config", "user.email")
	email, err := emailCmd.Output()
	if err != nil {
		fmt.Println("⚠️ Couldn't get user email, skipping author commits")
		return ""
	}
	authorEmail := strings.TrimSpace(string(email))

	// Get recent commits by the author
	cmd := exec.Command("git", "log", "--author="+authorEmail, "--pretty=format:%B", "-n", "20")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("⚠️ Couldn't fetch recent commits, skipping author commits")
		return ""
	}

	// Split by commit boundaries and filter
	commitMsgs := strings.Split(string(output), "\n\n")

	// Filter to only include messages with more than just a title and optional sign-off-by
	filteredMsgs := []string{}
	for _, msg := range commitMsgs {
		lines := strings.Split(strings.TrimSpace(msg), "\n")

		// Skip if it's just a title or title + sign-off
		if len(lines) <= 1 || (len(lines) == 2 && strings.HasPrefix(lines[1], "Signed-off-by:")) {
			continue
		}

		filteredMsgs = append(filteredMsgs, msg)
		if len(filteredMsgs) >= 5 {
			break
		}
	}

	return strings.Join(filteredMsgs, "\n\n---\n\n")
}

func generateCommitMessage(diff, files, apiKey string) string {
	fmt.Println("🤖 Generating commit message...")

	// Basic prompt with diff and changed files
	promptText := fmt.Sprintf(`
		Here are the changed files:
		%s

		Here is the diff:
		%s`, files, diff)

	// Read system prompt from embedded file
	systemRole, err := readPromptFile()
	if err != nil {
		return ""
	}

	// Prepare request
	messages := []Message{
		{Role: "system", Content: systemRole},
		{Role: "user", Content: promptText},
	}

	requestData := OpenAIRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: 0.3,
	}

	jsonData, err := json.Marshal(requestData)
	if err != nil {
		fmt.Printf("❌ Error creating JSON request: %s\n", err)
		return ""
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", apiEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Printf("❌ Error creating HTTP request: %s\n", err)
		return ""
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Error sending request: %s\n", err)
		return ""
	}
	defer resp.Body.Close()

	// Process response
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("❌ API error (status %d): %s\n", resp.StatusCode, body)
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("❌ Error reading response: %s\n", err)
		return ""
	}

	var openAIResp OpenAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		fmt.Printf("❌ Error parsing response: %s\n", err)
		return ""
	}

	if len(openAIResp.Choices) == 0 {
		fmt.Println("❌ No message generated")
		return ""
	}

	message := openAIResp.Choices[0].Message.Content
	message = strings.TrimSpace(message)

	// Clean up message - remove quotes if API returned them
	reQuotes := regexp.MustCompile(`^["'](.*)["']$`)
	if matches := reQuotes.FindStringSubmatch(message); len(matches) > 1 {
		message = matches[1]
	}

	// Strip markdown code fences if present
	message = stripMarkdownFences(message)

	return message
}

func readPromptFile() (string, error) {
	// Parse the prompt as a Go template
	tmpl, err := template.New("systemprompt").Parse(systemPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}

	promptData := struct {
		LastFiveCommits string
	}{
		LastFiveCommits: getCurrentAuthorRecentCommits(),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData); err != nil {
		return "", fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

func stripMarkdownFences(message string) string {
	// Check if message starts with markdown code fence
	if strings.HasPrefix(strings.TrimSpace(message), "```") {
		lines := strings.Split(message, "\n")
		if len(lines) <= 1 {
			// Single line with just the fence, return empty
			return ""
		}

		// Remove the first line (opening fence)
		lines = lines[1:]

		// If the last line is a closing fence, remove it
		lastIdx := len(lines) - 1
		if lastIdx >= 0 && strings.TrimSpace(lines[lastIdx]) == "```" {
			lines = lines[:lastIdx]
		}

		return strings.TrimSpace(strings.Join(lines, "\n"))
	}

	return message
}

func updateCommitMessageFile(message, commitMsgFile string) {
	existingContent, err := os.ReadFile(commitMsgFile)
	if err != nil {
		fmt.Printf("❌ Error reading commit message file: %s\n", err)
		return
	}

	// Combine generated message with existing content
	newContent := fmt.Sprintf("%s\n\n%s", message, string(existingContent))

	err = os.WriteFile(commitMsgFile, []byte(newContent), 0644)
	if err != nil {
		fmt.Printf("❌ Error writing commit message file: %s\n", err)
	}
}
