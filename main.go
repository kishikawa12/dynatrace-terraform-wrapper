/**
* @license
* Copyright 2020 Dynatrace LLC
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package main

import (
	"archive/zip"
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const terraformVersion = "1.9.8"
const configFileName = "wrapper.cfg"
const logFileName = "terraform.log"

// ============================================================
// Check for Terraform executable - $PATH or download locally
// ============================================================

// Check for Terraform executable; download if missing
func checkTerraformExecutable() (string, error) {
	executable := "terraform"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}

	if path, err := exec.LookPath(executable); err == nil {
		fmt.Println("Terraform found in PATH.")
		return path, nil
	}

	if _, err := os.Stat(executable); err == nil {
		fmt.Println("Terraform executable found in the current directory.")
		if runtime.GOOS != "windows" {
			executable = "./" + executable // Prepend './' for Unix
		}
		return executable, nil
	}

	fmt.Println("Terraform not found in PATH or current directory. Downloading...")
	if err := downloadTerraform(); err != nil {
		return "", fmt.Errorf("failed to download Terraform: %w", err)
	}

	return unzipTerraform("terraform.zip")
}

// Download Terraform zip file
func downloadTerraform() error {
	url := fmt.Sprintf("https://releases.hashicorp.com/terraform/%s/terraform_%s_%s_%s.zip", terraformVersion, terraformVersion, runtime.GOOS, runtime.GOARCH)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	out, err := os.Create("terraform.zip")
	if err != nil {
		return fmt.Errorf("failed to create file for Terraform zip: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// Unzip Terraform and return the executable path
func unzipTerraform(zipPath string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var execPath string
	for _, f := range r.File {
		filePath := filepath.Join(".", f.Name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, os.ModePerm); err != nil {
				return "", err
			}
			continue
		}
		if err := extractFile(f, filePath); err != nil {
			return "", err
		}
		execPath = filePath
	}

	os.Remove(zipPath)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(execPath, 0755); err != nil {
			return "", err
		}
		execPath = "./" + execPath // Prepend './' for Unix
	}

	return execPath, nil
}

// Extract individual file from zip
func extractFile(f *zip.File, dest string) error {
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(out, rc)
	return err
}

// ============================================================
// Load prepackaged configuration
// ============================================================

// Load configuration from file
func loadConfig(fileName string) (map[string]string, bool, bool, error) {
	config := make(map[string]string)
	apiToken := false
	oauthClient := false

	file, err := os.Open(fileName)
	if err != nil {
		return nil, false, false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			config[key] = value

			if key == "api_token" && value == "true" {
				apiToken = true
			} else if key == "oauth_client" && value == "true" {
				oauthClient = true
			}
		}
	}

	return config, apiToken, oauthClient, scanner.Err()
}

// ============================================================
// Execute Terraform commands
// ============================================================

// Execute Terraform command
func executeTerraformCommand(terraformPath string, logFile *os.File, args ...string) error {
	if logFile != nil {
		args = append(args, "-no-color")
	}

	var cmd *exec.Cmd
	if runtime.GOOS != "windows" {
		cmd = exec.Command(terraformPath, args...)
	} else {
		cmd = exec.Command("cmd.exe", "/C", terraformPath)
		cmd.Args = append(cmd.Args, args...)
	}

	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	return cmd.Run()
}

// Initialize the Terraform working directory
func initTerraform(terraformPath string, logFile *os.File) error {
	return executeTerraformCommand(terraformPath, logFile, "init")
}

// Run a Terraform plan to preview configuration
func previewConfiguration(terraformPath string, logFile *os.File) error {
	return executeTerraformCommand(terraformPath, logFile, "plan")
}

// Run a Terraform apply to publish configuration
func publishConfiguration(terraformPath string, logFile *os.File) error {
	return executeTerraformCommand(terraformPath, logFile, "apply", "-auto-approve")
}

// Run a Terraform destroy to remove configuration
func removeConfiguration(terraformPath string, logFile *os.File) error {
	return executeTerraformCommand(terraformPath, logFile, "destroy", "-auto-approve")
}

// ============================================================
// Set environment variables from config file or prompt
// ============================================================

// Helper function to set environment variable from config or prompt if missing
func setEnvFromConfigOrPrompt(envKey, promptMsg string, config map[string]string, reader *bufio.Reader) {
	if _, isSet := os.LookupEnv(envKey); isSet {
		return
	}

	if configValue, found := config[envKey]; found {
		os.Setenv(envKey, configValue)
		return
	}

	fmt.Print(promptMsg)
	inputValue, _ := reader.ReadString('\n')
	inputValue = strings.TrimSpace(inputValue)
	os.Setenv(envKey, inputValue)
}

// Set environment variables based on config file or prompt if missing
func setEnvironmentVars(config map[string]string, apiToken, oauthClient bool) {
	reader := bufio.NewReader(os.Stdin)

	if apiToken {
		setEnvFromConfigOrPrompt("DT_ENV_URL", "Input Dynatrace environment URL (SaaS: https://########.live.dynatrace.com or Managed: https://<dynatrace-host>/e/########): ", config, reader)
		setEnvFromConfigOrPrompt("DT_API_TOKEN", "Input Dynatrace API token (dt0c01.########.########): ", config, reader)
	}

	if oauthClient {
		setEnvFromConfigOrPrompt("DT_CLIENT_ID", "Input Dynatrace OAuth client ID (dt0s02.########): ", config, reader)
		setEnvFromConfigOrPrompt("DT_CLIENT_SECRET", "Input Dynatrace OAuth client secret (dt0s02.########.########): ", config, reader)
		setEnvFromConfigOrPrompt("DT_ACCOUNT_ID", "Input Dynatrace OAuth account ID (urn:dtaccount:{your-account-UUID}): ", config, reader)
	}
}

// ============================================================
// Display menu
// ============================================================

// Display menu and handle user input
func displayMenu(terraformPath string, logFile *os.File) {
	for {
		fmt.Println("\n--------------------------")
		fmt.Println("Select an option:")
		fmt.Println("1. Preview configuration (terraform plan)")
		fmt.Println("2. Publish configuration (terraform apply)")
		fmt.Println("3. Remove configuration (terraform destroy)")
		fmt.Println("4. Exit")
		fmt.Print("Enter your choice: ")

		reader := bufio.NewReader(os.Stdin)
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			fmt.Println("\nRunning Terraform plan to preview configuration...")
			if err := previewConfiguration(terraformPath, logFile); err != nil {
				log.Printf("Failed to preview configuration: %v\n", err)
			}
			fmt.Println("Completed Terraform plan.")
		case "2":
			fmt.Println("\nRunning Terraform apply to publish configuration...")
			if err := publishConfiguration(terraformPath, logFile); err != nil {
				log.Printf("Failed to publish configuration: %v\n", err)
			}
			fmt.Println("Completed Terraform apply.")
		case "3":
			fmt.Println("\nRunning Terraform destroy to remove configuration...")
			if err := removeConfiguration(terraformPath, logFile); err != nil {
				log.Printf("Failed to remove configuration: %v\n", err)
			}
			fmt.Println("Completed Terraform destroy.")
		case "4":
			fmt.Println("Exiting.")
			return
		default:
			fmt.Println("Invalid choice. Please enter 1, 2, 3, or 4.")
		}
	}
}

// ============================================================

func main() {
	applyFlag := flag.Bool("apply", false, "Run 'terraform apply' to publish configuration without menu")
	destroyFlag := flag.Bool("destroy", false, "Run 'terraform destroy' to remove configuration without menu")
	consoleFlag := flag.Bool("console", false, "Output Terraform stdout/stderr onto console instead of log file")
	flag.Parse()

	if *applyFlag && *destroyFlag {
		log.Fatal("Cannot use both -apply and -destroy flags simultaneously.")
	}

	terraformPath, err := checkTerraformExecutable()
	if err != nil {
		log.Fatalf("Error preparing Terraform executable: %v", err)
	}

	config, apiToken, oauthClient, err := loadConfig(configFileName)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	var logFile *os.File
	if !*consoleFlag {
		fmt.Printf("Redirecting Terraform output to %s...\n", logFileName)
		logFile, err = os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		defer logFile.Close()
	}

	setEnvironmentVars(config, apiToken, oauthClient)

	if err := initTerraform(terraformPath, logFile); err != nil {
		log.Fatalf("Error initializing Terraform: %v", err)
	}

	if *applyFlag {
		fmt.Println("\nRunning Terraform apply to publish configuration...")
		if err := publishConfiguration(terraformPath, logFile); err != nil {
			log.Fatalf("Failed to publish configuration: %v", err)
		}
		fmt.Println("Completed Terraform apply.")
		return
	}

	if *destroyFlag {
		fmt.Println("\nRunning Terraform destroy to remove configuration...")
		if err := removeConfiguration(terraformPath, logFile); err != nil {
			log.Fatalf("Failed to remove configuration: %v", err)
		}
		fmt.Println("Completed Terraform destroy.")
		return
	}

	displayMenu(terraformPath, logFile)
}
