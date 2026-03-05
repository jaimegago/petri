// Package commits generates realistic git commit history for lab repositories.
// Commits span several weeks and progress through recognisable engineering phases:
// initial setup → stabilisation → features → recent activity (with optional incidents).
package commits

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/jaimegago/petri/pkg/types"
)

// RepoType classifies the purpose of a git repository.
type RepoType string

const (
	// RepoTypeInfra is the IaC / infrastructure repository.
	RepoTypeInfra RepoType = "infra"
	// RepoTypeGitOps is the GitOps manifests repository.
	RepoTypeGitOps RepoType = "gitops"
	// RepoTypeApps is the application source / config repository.
	RepoTypeApps RepoType = "apps"
)

// CommitSpec describes a single git commit to be created.
type CommitSpec struct {
	Message   string
	Author    types.Author
	Timestamp time.Time
	// Files is a map of relative path → content. When empty the caller should
	// create a minimal placeholder file to satisfy git's empty-tree constraint.
	Files map[string]string
}

// GenerateOptions controls commit history generation.
type GenerateOptions struct {
	// RepoType selects the commit message vocabulary.
	RepoType RepoType
	// Company provides author personas and company metadata.
	Company *types.Company
	// Level controls the amount and complexity of history generated.
	Level int
	// Seed is used to initialise the PRNG for reproducible output.
	// When 0, time.Now().UnixNano() is used.
	Seed int64
}

// Generator creates realistic commit sequences for lab repositories.
type Generator interface {
	// Generate returns an ordered slice of CommitSpecs representing the full
	// git history for a single repository.
	Generate(ctx context.Context, opts GenerateOptions) ([]CommitSpec, error)
}

type generator struct{}

// New returns a new commit Generator.
func New() Generator {
	return &generator{}
}

// Generate builds a realistic commit history spanning ~4–8 weeks.
func (g *generator) Generate(_ context.Context, opts GenerateOptions) ([]CommitSpec, error) {
	if opts.Company == nil {
		return nil, fmt.Errorf("company must not be nil")
	}
	if len(opts.Company.Authors) == 0 {
		return nil, fmt.Errorf("company %q has no author personas", opts.Company.Name)
	}

	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // not used for security

	messages := messagesFor(opts.RepoType, opts.Company)
	now := time.Now()

	// Generate phases from oldest (furthest back) to newest.
	var commits []CommitSpec
	commits = append(commits, setupPhase(rng, now, opts, messages)...)
	commits = append(commits, stabilisationPhase(rng, now, opts, messages)...)
	commits = append(commits, featuresPhase(rng, now, opts, messages)...)
	commits = append(commits, recentPhase(rng, now, opts, messages)...)

	return commits, nil
}

// ─── Phase builders ───────────────────────────────────────────────────────────

// setupPhase creates 3–5 commits 5–8 weeks ago.
func setupPhase(rng *rand.Rand, now time.Time, opts GenerateOptions, msgs phaseMessages) []CommitSpec {
	count := 3 + rng.Intn(3)
	base := now.AddDate(0, 0, -(35 + rng.Intn(21)))
	return buildCommits(rng, base, count, msgs.setup, opts.Company.Authors, opts.Level)
}

// stabilisationPhase creates 4–7 commits 3–5 weeks ago.
func stabilisationPhase(rng *rand.Rand, now time.Time, opts GenerateOptions, msgs phaseMessages) []CommitSpec {
	count := 4 + rng.Intn(4)
	base := now.AddDate(0, 0, -(21 + rng.Intn(14)))
	return buildCommits(rng, base, count, msgs.stabilise, opts.Company.Authors, opts.Level)
}

// featuresPhase creates 4–8 commits 1–3 weeks ago; extra commits at Level 2+.
func featuresPhase(rng *rand.Rand, now time.Time, opts GenerateOptions, msgs phaseMessages) []CommitSpec {
	count := 4 + rng.Intn(5)
	if opts.Level >= 2 {
		count += 2
	}
	base := now.AddDate(0, 0, -(7 + rng.Intn(14)))
	return buildCommits(rng, base, count, msgs.features, opts.Company.Authors, opts.Level)
}

// recentPhase creates 2–5 commits in the last week; Level 3 may include incidents.
func recentPhase(rng *rand.Rand, now time.Time, opts GenerateOptions, msgs phaseMessages) []CommitSpec {
	count := 2 + rng.Intn(4)
	base := now.AddDate(0, 0, -6)
	recent := buildCommits(rng, base, count, msgs.recent, opts.Company.Authors, opts.Level)

	// At Level 3 there's a ~40% chance of a brief incident response sequence.
	if opts.Level >= 3 && rng.Intn(10) < 4 {
		incident := buildCommits(rng, now.AddDate(0, 0, -2), 2, msgs.incident, opts.Company.Authors, opts.Level)
		recent = append(recent, incident...)
	}
	return recent
}

// buildCommits returns count CommitSpecs spread across a time window starting at base.
func buildCommits(rng *rand.Rand, base time.Time, count int, msgs []string, authors []types.Author, level int) []CommitSpec {
	if len(msgs) == 0 {
		return nil
	}
	commits := make([]CommitSpec, 0, count)
	for i := 0; i < count; i++ {
		// Spread commits roughly every 8–20 hours within the window.
		offset := time.Duration(rng.Intn(count*12+1)) * time.Hour
		ts := base.Add(offset)

		// Clamp to business hours (7:00–22:00 UTC) for realism.
		ts = clampToWorkHours(ts)

		commits = append(commits, CommitSpec{
			Message:   msgs[rng.Intn(len(msgs))],
			Author:    authors[rng.Intn(len(authors))],
			Timestamp: ts,
			Files:     placeholderFiles(msgs[rng.Intn(len(msgs))], level),
		})
	}
	return commits
}

// clampToWorkHours adjusts a timestamp to a weekday hour in [7, 22).
func clampToWorkHours(t time.Time) time.Time {
	h := t.Hour()
	if h < 7 {
		t = t.Add(time.Duration(7-h) * time.Hour)
	} else if h >= 22 {
		t = t.Add(time.Duration(24-h+7) * time.Hour)
	}
	// Skip weekends (push to Monday).
	for t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		t = t.Add(24 * time.Hour)
	}
	return t
}

// placeholderFiles returns a single README-style change to make the commit non-empty.
func placeholderFiles(_ string, _ int) map[string]string {
	return map[string]string{
		".petri-meta": fmt.Sprintf("generated-by: petri\n"),
	}
}

// ─── Message vocabulary ───────────────────────────────────────────────────────

type phaseMessages struct {
	setup     []string
	stabilise []string
	features  []string
	recent    []string
	incident  []string
}

func messagesFor(rt RepoType, company *types.Company) phaseMessages {
	switch rt {
	case RepoTypeGitOps:
		return gitopsMessages(company)
	case RepoTypeApps:
		return appsMessages(company)
	default:
		return infraMessages(company)
	}
}

func infraMessages(company *types.Company) phaseMessages {
	cloud := string(company.CloudProvider)
	tool := string(company.IaCTool)

	// Company-specific setup messages add flavour matching the org's tech stack.
	setupExtra := companyInfraSetup(company)
	featuresExtra := companyInfraFeatures(company)

	setup := append([]string{
		fmt.Sprintf("init: bootstrap %s %s configuration", cloud, tool),
		"init: add provider config and backend",
		"init: add VPC and networking modules",
		"init: add initial cluster configuration",
		fmt.Sprintf("init: configure %s state backend", cloud),
		"docs: add README with setup instructions",
	}, setupExtra...)

	features := append([]string{
		"feat: add cluster autoscaler configuration",
		"feat: enable private endpoint access",
		"feat: add managed database module",
		"feat: configure node pools per environment",
		"feat: add cache layer (Redis) configuration",
		"feat: enable container insights monitoring",
		"feat: add spot instance node group",
		"chore: upgrade " + tool + " modules to latest",
	}, featuresExtra...)

	return phaseMessages{
		setup: setup,
		stabilise: []string{
			"fix: correct subnet CIDR ranges",
			"fix: update IAM role permissions",
			"fix: resolve provider version constraints",
			"chore: pin module versions for stability",
			"fix: add missing tags to resources",
			fmt.Sprintf("chore: run %s fmt", tool),
			"fix: correct security group ingress rules",
			"refactor: extract VPC config to module",
		},
		features: features,
		recent: []string{
			"chore: rotate service account keys",
			"fix: update node instance type for cost savings",
			"chore: add cost allocation tags",
			"chore: weekly " + tool + " plan review",
			"fix: adjust autoscaler thresholds",
		},
		incident: []string{
			"hotfix: increase cluster node count after OOM events",
			"hotfix: update security group to fix connectivity",
			"post-incident: apply hardened config after review",
			"hotfix: restore backup configuration after drift detected",
		},
	}
}

// companyInfraSetup returns company-specific init commit messages.
func companyInfraSetup(company *types.Company) []string {
	switch company.Name {
	case "acme":
		return []string{
			"init: configure EKS managed node groups",
			"init: add IAM roles for service accounts (IRSA)",
			"init: set up S3 remote state with DynamoDB locking",
		}
	case "techflow":
		return []string{
			"init: configure AKS with managed identity",
			"init: add Azure Monitor integration",
			"init: set up Azure Storage Account for Pulumi state",
		}
	case "cloudnative":
		return []string{
			"init: configure GKE Autopilot workload identity",
			"init: add Cloud Armor security policies",
			"init: set up GCS bucket for Terraform state",
		}
	default:
		return nil
	}
}

// companyInfraFeatures returns company-specific feature commit messages.
func companyInfraFeatures(company *types.Company) []string {
	switch company.Name {
	case "acme":
		return []string{
			"feat: enable EKS Pod Identity for workloads",
			"feat: add Karpenter node provisioner",
			"feat: configure AWS Load Balancer Controller",
			"feat: add ElastiCache Redis cluster module",
		}
	case "techflow":
		return []string{
			"feat: enable Azure AD workload identity federation",
			"feat: add KEDA HTTP scaler configuration",
			"feat: configure Azure Application Gateway Ingress",
			"feat: add Azure Cache for Redis module",
		}
	case "cloudnative":
		return []string{
			"feat: enable GKE Gateway API for traffic management",
			"feat: add Cloud Spanner instance module",
			"feat: configure Anthos Service Mesh (ASM)",
			"feat: add Memorystore Redis instance",
		}
	default:
		return nil
	}
}

func gitopsMessages(company *types.Company) phaseMessages {
	tool := string(company.GitOpsTool)
	return phaseMessages{
		setup: []string{
			fmt.Sprintf("init: bootstrap %s app-of-apps structure", tool),
			"init: add platform namespace definitions",
			"init: add initial application manifests",
			fmt.Sprintf("init: configure %s sync settings", tool),
			"init: add cluster-level kustomization",
			"docs: document gitops workflow",
		},
		stabilise: []string{
			"fix: correct image pull policy for staging",
			"fix: update resource limits after OOMKilled events",
			"chore: add missing readiness probes",
			"fix: align namespace labels with policy",
			"refactor: consolidate overlays structure",
			"fix: correct sync wave ordering",
		},
		features: []string{
			"feat: add HPA for frontend services",
			"feat: configure PodDisruptionBudgets",
			"feat: add network policies for service isolation",
			"feat: enable KEDA triggers for queue-based scaling",
			"feat: add Vault secret injection annotations",
			"feat: configure topology spread constraints",
			"feat: add observability stack manifests",
		},
		recent: []string{
			"chore: bump app images to latest digest",
			"fix: patch liveness probe timing after timeout spike",
			"chore: update cert-manager issuer config",
			"fix: correct ingress annotations",
			"chore: sync with upstream chart values",
		},
		incident: []string{
			"hotfix: rollback frontend to v1.2.3 after regression",
			"hotfix: scale checkout service after cart spike",
			"post-incident: add circuit breaker annotations",
			"hotfix: disable flaky canary after traffic loss",
		},
	}
}

func appsMessages(company *types.Company) phaseMessages {
	langExtra := companyAppFeatures(company)

	features := append([]string{
		"feat: add retry logic with exponential backoff",
		"feat: implement circuit breaker pattern",
		"feat: add distributed tracing spans",
		"feat: implement request validation middleware",
		"feat: add Prometheus metrics endpoint",
		"feat: implement graceful shutdown",
		"feat: add request ID propagation",
	}, langExtra...)

	return phaseMessages{
		setup: []string{
			"init: scaffold " + company.Name + " microservices",
			"init: add Dockerfile and build pipeline",
			"init: add health check endpoints",
			"init: configure structured logging",
			"init: add OpenTelemetry instrumentation",
			"docs: add API documentation",
		},
		stabilise: []string{
			"fix: handle nil pointer in request parser",
			"fix: improve error messages for downstream failures",
			"chore: add unit tests for core handlers",
			"fix: resolve race condition in cache layer",
			"chore: add integration test suite",
			"fix: correct Content-Type header handling",
		},
		features: features,
		recent: []string{
			"chore: update dependencies",
			"fix: patch CVE in base image",
			"perf: optimise DB query with index hint",
			"fix: reduce startup time",
			"chore: improve log sampling rate",
		},
		incident: []string{
			"hotfix: fix panic on malformed JSON payload",
			"hotfix: add request size limit after memory spike",
			"post-incident: add rate limiting after DDoS attempt",
			"hotfix: restore idempotent write after duplicate events",
		},
	}
}

// companyAppFeatures returns company-specific app feature commit messages.
func companyAppFeatures(company *types.Company) []string {
	switch company.Name {
	case "acme":
		return []string{
			"feat: add gRPC service-to-service authentication",
			"feat: implement Go slog structured logging",
			"feat: add pprof endpoint for profiling",
			"feat: migrate from net/http to chi router",
		}
	case "techflow":
		return []string{
			"feat: add ASP.NET Core middleware pipeline",
			"feat: implement Polly resilience policies",
			"feat: add MassTransit message bus integration",
			"feat: configure OpenAPI (Swagger) docs",
		}
	case "cloudnative":
		return []string{
			"feat: add Spring Boot Actuator health endpoints",
			"feat: implement Spring Cloud Circuit Breaker",
			"feat: add Micrometer metrics with Prometheus registry",
			"feat: configure Spring Boot tracing with Zipkin",
		}
	default:
		return nil
	}
}
