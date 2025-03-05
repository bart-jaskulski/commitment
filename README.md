# Commitment

AI-powered Git commit message generator using Gemini. Automatically creates meaningful commit messages based on your staged changes.

## Setup

1. Install the tool:
   ```
   go install github.com/bart-jaskulski/commitment@latest
   ```

2. Set up the Git hook:
   ```
   commitment install
   ```

3. Set your Gemini API key:
   ```
   export GEMINI_API_KEY=your_api_key_here
   ```

## Usage

Just use `git commit` as normal. Commitment will automatically generate a commit message based on your staged changes.

## How It Works

Commitment analyzes your git diff, feeds it to the Gemini API, and prepends the generated message to your commit message file.
