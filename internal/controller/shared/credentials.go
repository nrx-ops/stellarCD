/*
Copyright 2026 nrx-ops.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package shared

// Well-known keys read from a VCS credentials Secret. A Secret carries exactly
// one of these layouts; DetectAuthMethod gives the precedence between them. The
// GitHub App keys live in githubapp.go.
//
// The controller and the runner both depend on this layout: the controller
// mounts the Secret into the Flare's Job, except for a GitHub App, which it
// exchanges for a short-lived installation token first so the App's private
// key never reaches a pod that runs repository code.
const (
	// SecretKeySSHPrivateKey holds a PEM private key for SSH clone URLs.
	SecretKeySSHPrivateKey = "sshPrivateKey"
	// SecretKeyUsername and SecretKeyPassword hold HTTP basic credentials.
	SecretKeyUsername = "username"
	SecretKeyPassword = "password"
	// SecretKeyToken is accepted as an alias for the password, which is what
	// GitHub, GitLab and Gitea personal access tokens actually are.
	SecretKeyToken = "token"
)

// GitHubAppUsername is the username GitHub documents for installation access
// tokens over HTTPS. The token goes in the password field.
const GitHubAppUsername = githubAppUsername
