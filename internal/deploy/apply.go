package deploy

// RunApply executes steps sequentially over an SSH runner, streaming
// output line-by-line, and reporting per-step status.

import (
	"bufio"
	"fmt"
	"strings"
)

// StepStatus is the per-step callback.
type StepStatus func(stepIndex int, title string, status string) // "running" | "ok" | "error" | "ignored"

// OnLine is called for each output line.
type OnLine func(line string)

// RunApply executes the full plan. It stops at the first non-Ignore
// failure and returns the combined output.
func RunApply(run Runner, steps []Step, onStep StepStatus, onLine OnLine) (string, error) {
	var combined strings.Builder

	for i, step := range steps {
		if onStep != nil {
			onStep(i, step.Title, "running")
		}

		cmd := step.Command
		if step.Sudo {
			cmd = "sudo -n bash -lc " + shellQuote(cmd)
		}

		output, err := run.Run(cmd, false)
		text := strings.TrimSpace(output)
		combined.WriteString(step.Title + "\n")
		combined.WriteString(text + "\n\n")

		if onLine != nil {
			scanner := bufio.NewScanner(strings.NewReader(output))
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MiB lines
			for scanner.Scan() {
				onLine(scanner.Text())
			}
			if err != nil {
				onLine("ERROR: " + err.Error())
			}
		}

		if err != nil {
			if step.Ignore {
				if onStep != nil {
					onStep(i, step.Title, "ignored")
				}
				continue
			}
			if onStep != nil {
				onStep(i, step.Title, "error")
			}
			return combined.String(), fmt.Errorf("step %d (%s): %w\n%s", i, step.Title, err, tailLines(text, 10))
		}

		if onStep != nil {
			onStep(i, step.Title, "ok")
		}
	}
	return combined.String(), nil
}

// shellQuote makes one argument safe for a remote `sh -c` invocation by
// single-quoting it; an embedded single quote is closed, backslash-escaped,
// and the span reopened — the byte sequence '\” — via a replacer.
func shellQuote(s string) string {
	quote := strings.NewReplacer("'", `'\''`)
	return "'" + quote.Replace(s) + "'"
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
