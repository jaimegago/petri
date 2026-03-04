package local

import "context"

// cliDocker implements dockerOps by shelling out to the Docker CLI.
type cliDocker struct{}

// ping checks that the Docker daemon is reachable by running `docker info`.
func (d *cliDocker) ping(ctx context.Context) error {
	return runCmd(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
}
