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

package adminapi

import (
	"net/http"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/nrx-ops/stellarCD/api/v1alpha1"
	"github.com/nrx-ops/stellarCD/internal/controller/shared"
)

// ScanRequest asks which root modules or Terragrunt units a repository holds,
// so the dashboard can import them as Astrals in one go.
type ScanRequest struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
	// Path scopes the scan to a folder of the repository.
	Path string `json:"path,omitempty"`
	// Secret holds the Git credentials. When empty, the Galaxy's are used.
	Secret string `json:"secret,omitempty"`
	// Galaxy names the Galaxy the Astrals will join, for its credentials.
	Galaxy string `json:"galaxy,omitempty"`
	// Terragrunt looks for terragrunt.hcl units instead of .tf root modules.
	Terragrunt  bool     `json:"terragrunt,omitempty"`
	Include     []string `json:"include,omitempty"`
	Exclude     []string `json:"exclude,omitempty"`
	StripPrefix string   `json:"stripPrefix,omitempty"`
}

// ScannedModule is one importable folder.
type ScannedModule struct {
	// Path is the folder, relative to the repository root.
	Path string `json:"path"`
	// Marker is the file that identified it, e.g. "terragrunt.hcl".
	Marker string `json:"marker"`
	// Name is the Astral name the folder maps to.
	Name string `json:"name"`
	// Existing is set when an Astral already bears that name.
	Existing bool `json:"existing,omitempty"`
}

// ScanResult lists what a repository holds.
type ScanResult struct {
	Modules []ScannedModule `json:"modules"`
	// Truncated reports that the host listed only part of the tree.
	Truncated bool `json:"truncated,omitempty"`
}

// handleScanRepository lists the modules of a repository through the host's
// tree API, the way Galaxy discovery does, without creating anything. Only
// GitHub repositories can be listed today.
func (s *Server) handleScanRepository(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.credentialsNamespace(w, r)
	if !ok {
		return
	}
	var req ScanRequest
	if !s.decodeStrict(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		s.fail(w, r, http.StatusBadRequest, "url", "Enter a Git repository")
		return
	}

	secretName := req.Secret
	if secretName == "" && req.Galaxy != "" {
		var galaxy corev1alpha1.Galaxy
		if err := s.Reader.Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: req.Galaxy}, &galaxy); err != nil {
			s.writeError(w, r, err, "This Galaxy could not be read.")
			return
		}
		if galaxy.Spec.VCSSecretRef != nil {
			secretName = galaxy.Spec.VCSSecretRef.Name
		}
	}
	var creds map[string][]byte
	if secretName != "" {
		secret, ok := s.readCredential(w, r, client.ObjectKey{Namespace: namespace, Name: secretName})
		if !ok {
			return
		}
		creds = secret.Data
	}

	tree, err := shared.ListRepositoryTree(r.Context(), req.URL, req.Branch, creds)
	if err != nil {
		// The cause may quote the host's answer; it goes to the log only.
		s.Log.Error(err, "Failed to list a repository for import", "namespace", namespace)
		s.fail(w, r, http.StatusBadGateway, "url",
			"This repository could not be listed. Check the URL, the branch and the credentials. "+
				"Only GitHub repositories can be scanned today.")
		return
	}

	engine := string(corev1alpha1.EngineTerraform)
	if req.Terragrunt {
		engine = "Terragrunt"
	}
	modules := shared.DetectModules(tree.Files, engine)
	include := req.Include
	if root := strings.Trim(strings.TrimSpace(req.Path), "/"); root != "" && root != "." {
		include = append([]string{root}, include...)
	}
	modules = shared.FilterModules(modules, include, req.Exclude)
	names := shared.NameModules(modules, req.StripPrefix)

	var astrals corev1alpha1.AstralList
	if err := s.Reader.List(r.Context(), &astrals, client.InNamespace(namespace)); err != nil {
		s.writeError(w, r, err, "Failed to list Astrals.")
		return
	}
	taken := make(map[string]bool, len(astrals.Items))
	for i := range astrals.Items {
		taken[astrals.Items[i].Name] = true
	}

	out := ScanResult{Modules: make([]ScannedModule, 0, len(modules)), Truncated: tree.Truncated}
	for _, m := range modules {
		name := names[m.Path]
		out.Modules = append(out.Modules, ScannedModule{Path: m.Path, Marker: m.Marker, Name: name, Existing: taken[name]})
	}
	s.writeJSON(w, r, out)
}
