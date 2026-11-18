package generators

// Deployment names a Deployment a rendered component creates. A caller that
// applied the component waits on it to know the component is running, which
// an accepted manifest alone does not say.
type Deployment struct {
	Namespace string
	Name      string
}
