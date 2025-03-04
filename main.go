package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"text/template"
	"time"
)

const (
	maxDiffLines  = 2000
	skipFlag      = "--no-ai-msg"
	maxTokens     = 150
	apiEndpoint = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	model = "gemini-2.0-flash"
)

type OpenAIRequest struct {
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	MaxTokens   int        `json:"max_tokens"`
	Temperature float64    `json:"temperature"`
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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Error: No commit message file provided")
		os.Exit(1)
	}

	commitMsgFile := os.Args[1]
	commitType := ""
	if len(os.Args) > 2 {
		commitType = os.Args[2]
	}

	// Skip in these cases
	if shouldSkip(commitType, commitMsgFile) {
		fmt.Println("⚠️ Skipping commit message generation")
		os.Exit(0)
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		fmt.Println("⚠️ GEMINI_API_KEY not set, skipping commit message generation")
		os.Exit(0)
	}

	// Get diff and changed files
	diff, diffLines := getGitDiff()
	if diff == "" {
		fmt.Println("⚠️ No changes to commit")
		// No changes to commit
		os.Exit(0)
	}

	changedFiles := getChangedFiles()

	// Check if diff is too large
	if diffLines > maxDiffLines {
		fmt.Printf("⚠️ Warning: Large diff detected (%d lines)\n", diffLines)
		fmt.Print("Do you want to generate a commit message anyway? (y/N): ")
		var response string
		fmt.Scanln(&response)
		if !strings.HasPrefix(strings.ToLower(response), "y") {
			fmt.Println("⏭️ Skipping message generation for large diff")
			os.Exit(0)
		}
	}

	// Generate message
	message := generateCommitMessage(diff, changedFiles, apiKey)
	if message != "" {
		fmt.Println("🤖 Updating commit message file...")
		updateCommitMessageFile(message, commitMsgFile)
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

	// Check if skip flag is in git arguments
	psCmd := exec.Command("ps", "-o", "args=", "-p", fmt.Sprintf("%d", os.Getppid()))
	output, err := psCmd.Output()
	if err == nil && strings.Contains(string(output), skipFlag) {
		return true
	}

	return false
}

func getGitDiff() (string, int) {
	cmd := exec.Command("git", "diff", "--staged")
	output, err := cmd.Output()
	if err != nil {
		return "", 0
	}
	
	diff := string(output)
	return diff, strings.Count(diff, "\n")
}

func getChangedFiles() string {
	cmd := exec.Command("git", "diff", "--staged", "--name-status")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	
	return string(output)
}

func generateCommitMessage(diff, files, apiKey string) string {
	fmt.Println("🤖 Generating commit message...")
	startTime := time.Now()

	// Limit diff size to avoid token limits
	if len(diff) > 4000 {
		diff = diff[:4000]
	}

	prompt := fmt.Sprintf(`
Here are the changed files:
%s

Here is the diff:
%s
`, files, diff)

	// Read system prompt from the prompt file
	systemRole, err := readPromptFile("prompt")
	if err != nil || systemRole == "" {
		systemRole = ""
	}

	// Prepare request
	messages := []Message{
		{Role: "system", Content: systemRole},
		{Role: "user", Content: prompt},
	}

	requestData := OpenAIRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: 0.7,
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

	elapsed := time.Since(startTime)
	fmt.Printf("✅ Message generated in %.2fs\n", elapsed.Seconds())

	return message
}

func readPromptFile(promptFile string) (string, error) {
	content, err := os.ReadFile(promptFile)
	if err != nil {
		return "", fmt.Errorf("failed to read prompt file: %w", err)
	}
	
	// Parse the prompt as a Go template
	tmpl, err := template.New("systemprompt").Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}
	
	// Execute the template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return "", fmt.Errorf("failed to execute prompt template: %w", err)
	}
	
	return strings.TrimSpace(buf.String()), nil
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
