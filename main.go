package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func killProcess(processName string) {
	currentPID := os.Getpid()
	cmd := exec.Command("powershell", "-Command", fmt.Sprintf("Get-Process -Name '%s' -ErrorAction SilentlyContinue | Select-Object Id", strings.TrimSuffix(processName, ".exe")))
	output, err := cmd.Output()
	if err != nil {
		// Ignore error as it might mean no process found
		log.Printf("No running process found for %s", processName)
		return
	}

	// Parse PowerShell output
	lines := strings.Split(string(output), "\n")
	foundProcess := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "Id") {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil {
			if pid != currentPID {
				foundProcess = true
				log.Printf("Found process %s with PID %d, attempting to kill...", processName, pid)
				killCmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid))
				if err := killCmd.Run(); err != nil {
					log.Printf("Warning: Error killing %s (PID %d): %v", processName, pid, err)
				} else {
					log.Printf("Successfully killed %s (PID %d)", processName, pid)
				}
			} else {
				log.Printf("Skipping current process (PID %d)", pid)
			}
		}
	}
	if !foundProcess {
		log.Printf("No other %s processes found to kill", processName)
	}
}

// parseAutoRestart extracts auto-restart value and filters out this argument
func parseAutoRestart(args []string) ([]string, int) {
	var filteredArgs []string
	autoRestartSeconds := 0

	// Parse all --key=value arguments
	for _, arg := range args {
		// Skip non-flag arguments
		if !strings.HasPrefix(arg, "--") {
			filteredArgs = append(filteredArgs, arg)
			continue
		}

		// Split into key-value
		parts := strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)
		if len(parts) != 2 {
			filteredArgs = append(filteredArgs, arg)
			continue
		}

		key, value := parts[0], parts[1]

		// Handle auto-restart separately
		if key == "auto-restart" {
			if val, err := strconv.Atoi(value); err == nil {
				autoRestartSeconds = val
			} else {
				log.Printf("Warning: Invalid auto-restart value: %s", value)
			}
			continue
		}

		// Keep other flags
		filteredArgs = append(filteredArgs, arg)
	}

	return filteredArgs, autoRestartSeconds
}

// checkAndUpdateExecutable checks for new version and updates if available
func checkAndUpdateExecutable(exePath string) error {
	newExePath := "./.main.exe.new"

	// Check if new version exists
	if _, err := os.Stat(newExePath); os.IsNotExist(err) {
		return nil // No update available
	}

	log.Println("Found new version, attempting to update...")

	// Get current process ID to avoid killing ourselves
	currentPID := os.Getpid()
	log.Printf("Current process ID: %d", currentPID)

	killProcess("main.exe")
	killProcess(".main.exe.new")

	// Wait for processes to fully terminate
	log.Println("Waiting for processes to terminate...")
	time.Sleep(2 * time.Second)

	// Remove old executable if exists
	log.Printf("Attempting to remove old executable: %s", exePath)
	if err := os.Remove(exePath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: Error removing old executable: %v", err)
		} else {
			log.Println("Old executable does not exist, continuing...")
		}
	} else {
		log.Println("Successfully removed old executable")
	}

	// Wait a bit after removal
	time.Sleep(1 * time.Second)

	// Rename new executable
	log.Println("Attempting to rename new executable...")
	for attempts := 0; attempts < 3; attempts++ {
		err := os.Rename(newExePath, exePath)
		if err == nil {
			log.Println("Successfully updated executable")
			return nil
		}
		log.Printf("Attempt %d: Error renaming executable: %v", attempts+1, err)
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("failed to rename new executable after multiple attempts")
}

func main() {
	// Parse and filter out auto-restart argument
	args, autoRestartSeconds := parseAutoRestart(os.Args[1:])
	exePath := "./main.exe"
	inactivityDuration := 10 * time.Minute

	log.Printf("Auto-restart interval: %d seconds", autoRestartSeconds)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	exitChan := make(chan struct{})

	var currentCmd *exec.Cmd
	var cmdMutex sync.Mutex

	go func() {
		<-sigChan
		log.Println("Received Ctrl+C signal, exiting program...")

		cmdMutex.Lock()
		if currentCmd != nil && currentCmd.Process != nil {
			pidStr := strconv.Itoa(currentCmd.Process.Pid)
			err := exec.Command("taskkill", "/T", "/F", "/PID", pidStr).Run()
			if err != nil {
				log.Println("Error executing taskkill:", err)
			} else {
				log.Println("Successfully killed process and its child processes.")
			}
		}
		cmdMutex.Unlock()

		close(exitChan)
	}()

	for {
		select {
		case <-exitChan:
			log.Println("Program terminated.")
			return
		default:
			// Check for updates before starting the process
			// if err := checkAndUpdateExecutable(exePath); err != nil {
			// 	log.Printf("Error checking/updating executable: %v", err)
			// }
			killProcess("main.exe")

			log.Println("Starting process:", exePath, "with arguments:", args)
			cmd := exec.Command(exePath, args...)

			cmdMutex.Lock()
			currentCmd = cmd
			cmdMutex.Unlock()

			stdout, err := cmd.StdoutPipe()
			if err != nil {
				log.Println("Error getting stdout pipe:", err)
				continue
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				log.Println("Error getting stderr pipe:", err)
				continue
			}

			if err := cmd.Start(); err != nil {
				log.Println("Error starting child process:", err)
				continue
			}

			outputCh := make(chan struct{}, 1)
			wg := sync.WaitGroup{}
			wg.Add(2)

			readOutput := func(r io.ReadCloser) {
				defer wg.Done()
				scanner := bufio.NewScanner(r)
				for scanner.Scan() {
					line := scanner.Text()
					log.Println(line)
					select {
					case outputCh <- struct{}{}:
					default:
					}
				}
			}

			go readOutput(stdout)
			go readOutput(stderr)

			stopMonitor := make(chan struct{})
			monitorDone := make(chan struct{})

			// Auto-restart timer goroutine
			if autoRestartSeconds > 0 {
				go func() {
					timer := time.NewTimer(time.Duration(autoRestartSeconds) * time.Second)
					defer timer.Stop()

					select {
					case <-timer.C:
						log.Printf("Auto-restart timer (%d seconds) expired. Killing process...", autoRestartSeconds)
						if cmd.Process != nil {
							pidStr := strconv.Itoa(cmd.Process.Pid)
							err := exec.Command("taskkill", "/T", "/F", "/PID", pidStr).Run()
							if err != nil {
								log.Println("Error executing taskkill:", err)
							} else {
								log.Println("Successfully killed process for auto-restart.")
							}
						}
					case <-stopMonitor:
						return
					}
				}()
			}

			// Inactivity monitor goroutine
			go func() {
				defer close(monitorDone)
				timer := time.NewTimer(inactivityDuration)
				defer timer.Stop()

				for {
					select {
					case <-outputCh:
						if !timer.Stop() {
							<-timer.C
						}
						timer.Reset(inactivityDuration)
					case <-timer.C:
						log.Printf("No output for %v, process might be hung. Killing process...", inactivityDuration)
						if cmd.Process != nil {
							pidStr := strconv.Itoa(cmd.Process.Pid)
							err := exec.Command("taskkill", "/T", "/F", "/PID", pidStr).Run()
							if err != nil {
								log.Println("Error executing taskkill:", err)
							} else {
								log.Println("Successfully killed process and its child processes.")
							}
						}
						return
					case <-stopMonitor:
						return
					}
				}
			}()

			err = cmd.Wait()
			close(stopMonitor)
			<-monitorDone
			wg.Wait()

			if err != nil {
				log.Println("Child process terminated with error:", err)
			} else {
				log.Println("Child process terminated normally.")
			}

			cmdMutex.Lock()
			currentCmd = nil
			cmdMutex.Unlock()

			log.Println("Restarting process in 1 second...")
			time.Sleep(1 * time.Second)
		}
	}
}
