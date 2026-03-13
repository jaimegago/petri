package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/types"
)

func (c *CLI) newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <lab-name>",
		Short: "Show detailed information about a lab",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return c.runInfo(args[0])
		},
	}
}

func (c *CLI) runInfo(name string) error {
	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	lab, err := mgr.GetLabByName(context.Background(), name)
	if err != nil {
		return fmt.Errorf("lab %q not found", name)
	}

	resources, err := mgr.ListResources(context.Background(), lab.ID)
	if err != nil {
		return fmt.Errorf("listing resources: %w", err)
	}

	// Load level spec for access info (best-effort; don't fail if unavailable).
	var spec *types.LevelSpec
	companiesPath := c.resolveCompaniesFile()
	if companies, loadErr := config.LoadCompanies(companiesPath); loadErr == nil {
		for i := range companies {
			if strings.EqualFold(companies[i].Name, lab.Company) {
				if s, specErr := companies[i].GetLevel(lab.Level); specErr == nil {
					spec = &s
				}
				break
			}
		}
	}

	// ── Identity ──────────────────────────────────────────────────────────────
	fmt.Printf("Lab: %s\n", lab.Name)
	fmt.Printf("  ID:       %s\n", lab.ID)
	fmt.Printf("  Company:  %s\n", lab.Company)
	fmt.Printf("  Level:    %d\n", lab.Level)
	fmt.Printf("  Provider: %s\n", lab.CloudProvider)
	fmt.Printf("  Status:   %s\n", lab.Status)
	fmt.Printf("  Created:  %s\n", lab.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Expires:  %s", lab.ExpiresAt.Format(time.RFC3339))
	if lab.IsExpired() {
		fmt.Print(" (EXPIRED)")
	}
	fmt.Println()

	if lab.Metadata.ErrorMessage != "" {
		fmt.Printf("  Error:    %s\n", lab.Metadata.ErrorMessage)
	}

	// ── Cluster access ────────────────────────────────────────────────────────
	if len(lab.Metadata.Clusters) > 0 {
		fmt.Println("\nClusters:")
		for _, cl := range lab.Metadata.Clusters {
			fmt.Printf("  %s (%d nodes)", cl.Name, cl.NodeCount)
			if cl.Endpoint != "" {
				fmt.Printf(" — %s", cl.Endpoint)
			}
			fmt.Println()
		}
		if lab.CloudProvider == types.CloudProviderLocal {
			kubeconfigPath := localKubeconfigPath(lab)
			if kubeconfigPath != "" {
				fmt.Println("\nAccess:")
				fmt.Printf("  export KUBECONFIG=%s\n", kubeconfigPath)
				fmt.Printf("  kubectl get pods -A\n")
			}
		}
	}

	// ── Git repos ─────────────────────────────────────────────────────────────
	if len(lab.Metadata.GitRepos) > 0 {
		fmt.Println("\nGit Repos:")
		for _, repo := range lab.Metadata.GitRepos {
			fmt.Printf("  [%s] %s — %s\n", repo.Type, repo.Name, repo.URL)
		}
	}

	// ── Applications ─────────────────────────────────────────────────────────
	if spec != nil && len(spec.Apps) > 0 {
		appNS := appInfoNamespace(spec)
		fmt.Printf("\nApplications (namespace: %s):\n", appNS)
		kubeconfigPath := localKubeconfigPath(lab)
		kcFlag := ""
		if kubeconfigPath != "" {
			kcFlag = fmt.Sprintf(" --kubeconfig=%s", kubeconfigPath)
		}
		for _, app := range spec.Apps {
			port := appInfoPort(lab.Company, app)
			fmt.Printf("  %-30s port %d\n", app, port)
			fmt.Printf("    kubectl%s port-forward -n %s svc/%s %d:%d\n", kcFlag, appNS, app, port, port)
			if isFrontendByName(app) && lab.CloudProvider == types.CloudProviderLocal {
				fmt.Printf("    Via ingress: http://localhost:30080\n")
			}
		}
	}

	// ── Platform ──────────────────────────────────────────────────────────────
	if spec != nil && len(spec.Platform) > 0 {
		fmt.Println("\nPlatform:")
		kubeconfigPath := localKubeconfigPath(lab)
		kcFlag := ""
		if kubeconfigPath != "" {
			kcFlag = fmt.Sprintf(" --kubeconfig=%s", kubeconfigPath)
		}
		hasIngress := false
		for _, p := range spec.Platform {
			switch strings.ToLower(p) {
			case "ingress-nginx":
				hasIngress = true
				fmt.Printf("  ingress-nginx    NodePort http://localhost:30080  https://localhost:30443\n")
			case "argocd":
				fmt.Printf("  argocd           kubectl%s port-forward -n argocd svc/argocd-server 8080:80\n", kcFlag)
				fmt.Printf("                   → http://localhost:8080  (admin / kubectl%s -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d)\n", kcFlag)
			case "cert-manager":
				fmt.Printf("  cert-manager     kubectl%s get certificates -A\n", kcFlag)
			case "flux":
				fmt.Printf("  flux             kubectl%s get kustomizations -A\n", kcFlag)
			default:
				fmt.Printf("  %s\n", p)
			}
		}
		if !hasIngress {
			_ = hasIngress // avoid unused warning; ingress line only printed when present
		}
	}

	// ── Observability ─────────────────────────────────────────────────────────
	if spec != nil && len(spec.Observability) > 0 {
		fmt.Println("\nObservability:")
		kubeconfigPath := localKubeconfigPath(lab)
		kcFlag := ""
		if kubeconfigPath != "" {
			kcFlag = fmt.Sprintf(" --kubeconfig=%s", kubeconfigPath)
		}
		for _, tool := range spec.Observability {
			switch strings.ToLower(tool) {
			case "prometheus":
				if lab.CloudProvider == types.CloudProviderLocal {
					fmt.Printf("  prometheus    http://localhost:30080/prometheus\n")
				} else {
					fmt.Printf("  prometheus    kubectl%s port-forward -n monitoring svc/prometheus 9090:9090\n", kcFlag)
					fmt.Printf("                → http://localhost:9090\n")
				}
			case "grafana":
				if lab.CloudProvider == types.CloudProviderLocal {
					fmt.Printf("  grafana       http://localhost:30080/grafana  (admin / petri-lab-admin)\n")
				} else {
					fmt.Printf("  grafana       kubectl%s port-forward -n monitoring svc/grafana 3000:3000\n", kcFlag)
					fmt.Printf("                → http://localhost:3000  (admin / petri-lab-admin)\n")
				}
			default:
				fmt.Printf("  %s\n", tool)
			}
		}
	}

	// ── Logs ──────────────────────────────────────────────────────────────────
	if spec != nil {
		appNS := appInfoNamespace(spec)
		kubeconfigPath := localKubeconfigPath(lab)
		kcFlag := ""
		if kubeconfigPath != "" {
			kcFlag = fmt.Sprintf(" --kubeconfig=%s", kubeconfigPath)
		}
		fmt.Println("\nLogs:")
		fmt.Printf("  # All pods in apps namespace\n")
		fmt.Printf("  kubectl%s logs -n %s -l app=<name> --follow\n", kcFlag, appNS)
		if len(spec.Apps) > 0 {
			fmt.Printf("\n  # Per-application\n")
			for _, app := range spec.Apps {
				fmt.Printf("  kubectl%s logs -n %s -l app=%s --follow\n", kcFlag, appNS, app)
			}
		}
		hasObs := false
		for _, tool := range spec.Observability {
			switch strings.ToLower(tool) {
			case "prometheus", "grafana":
				if !hasObs {
					fmt.Printf("\n  # Observability\n")
					hasObs = true
				}
				fmt.Printf("  kubectl%s logs -n monitoring -l app=%s --follow\n", kcFlag, strings.ToLower(tool))
			}
		}
	}

	// Static observability URLs stored in metadata (cloud labs).
	if len(lab.Metadata.ObservabilityURLs) > 0 {
		fmt.Println("\nObservability URLs:")
		for tool, url := range lab.Metadata.ObservabilityURLs {
			fmt.Printf("  %s: %s\n", tool, url)
		}
	}

	// ── Resources ─────────────────────────────────────────────────────────────
	if len(resources) > 0 {
		fmt.Printf("\nResources: %d tracked\n", len(resources))
		for _, r := range resources {
			fmt.Printf("  [%s] %s", r.ResourceType, r.ResourceID)
			if r.CloudResourceID != "" {
				fmt.Printf(" (%s)", r.CloudResourceID)
			}
			fmt.Println()
		}
	}

	return nil
}

// localKubeconfigPath returns the kubeconfig path for a local lab's first cluster.
func localKubeconfigPath(lab *types.Lab) string {
	if lab.CloudProvider != types.CloudProviderLocal {
		return ""
	}
	if len(lab.Metadata.Clusters) > 0 && lab.Metadata.Clusters[0].KubeconfigPath != "" {
		return lab.Metadata.Clusters[0].KubeconfigPath
	}
	return ""
}

// appInfoNamespace returns the Kubernetes namespace apps are deployed into.
func appInfoNamespace(spec *types.LevelSpec) string {
	if len(spec.Namespaces) > 0 {
		return spec.Namespaces[0]
	}
	return "default"
}

// appInfoPort returns the primary port for a named app, using per-company
// knowledge for well-known apps and falling back to 8080.
func appInfoPort(company, app string) int {
	type appKey struct{ company, app string }
	ports := map[appKey]int{
		// acme
		{"acme", "boutique-frontend"}:    8080,
		{"acme", "boutique-cart"}:        8080,
		{"acme", "boutique-checkout"}:    8080,
		{"acme", "online-boutique-full"}: 8080,
		{"acme", "payment-service-v2"}:   8080,
		{"acme", "inventory-service"}:    8080,
		{"acme", "notification-service"}: 8080,
		// techflow
		{"techflow", "api-gateway"}:          8080,
		{"techflow", "auth-service"}:         8081,
		{"techflow", "user-service"}:         8082,
		{"techflow", "order-service"}:        8083,
		{"techflow", "product-service"}:      8084,
		{"techflow", "notification-service"}: 8085,
		{"techflow", "reporting-service"}:    8086,
		{"techflow", "audit-service"}:        8087,
		// cloudnative
		{"cloudnative", "spring-frontend"}:     8080,
		{"cloudnative", "spring-catalog"}:      8081,
		{"cloudnative", "spring-cart"}:         8082,
		{"cloudnative", "spring-orders"}:       8083,
		{"cloudnative", "spring-payments"}:     8084,
		{"cloudnative", "spring-shipping"}:     8085,
		{"cloudnative", "spring-notifications"}: 8086,
	}
	if p, ok := ports[appKey{company, app}]; ok {
		return p
	}
	return 8080
}

// isFrontendByName returns true for apps that are the public-facing frontend.
func isFrontendByName(app string) bool {
	frontends := []string{"boutique-frontend", "online-boutique-full", "api-gateway", "spring-frontend", "frontend"}
	app = strings.ToLower(app)
	for _, f := range frontends {
		if app == f {
			return true
		}
	}
	return false
}
