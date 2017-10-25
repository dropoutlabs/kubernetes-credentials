package client

import (
	"context"
	"errors"
	"sync"

	manifold "github.com/manifoldco/go-manifold"
	"github.com/manifoldco/kubernetes-credentials/primitives"
)

var (
	ErrLabelRequired          = errors.New("A label is required to perform this query.")
	ErrResourceInvalid        = errors.New("The resource is invalid")
	ErrMultipleResourcesFound = errors.New("Multiple resources with the same label are found. Please provide a specific project.")

	ErrTeamNotFound            = errors.New("Team not found.")
	ErrResourceNotFound        = errors.New("A resource with the given label is not found.")
	ErrProjectNotFound         = errors.New("Project with the given label is not  found.")
	ErrCredentialNotFound      = errors.New("Credential with the given KEY is not found.")
	ErrCredentialNotSpecified  = errors.New("We've found a credential that you did not specify.")
	ErrCredentialDefaultNotSet = errors.New("You did not provide a default for a the non-existing credential.")
)

// Client is a wrapper around the manifold client.
type Client struct {
	sync.RWMutex
	cl         *manifold.Client
	team       *string
	teamID     *manifold.ID
	projectIDs map[string]*manifold.ID
}

// NewWithClient returns a new wrapper client with a Manifold client in it.
func New(cl *manifold.Client, team *string) (*Client, error) {
	c := &Client{
		cl:         cl,
		team:       team,
		projectIDs: map[string]*manifold.ID{},
	}
	return c, c.ensureTeamID()
}

// GetResource gets a resource for a specific label. If no resource is given,
// this will error.
func (c *Client) GetResource(ctx context.Context, project *string, res *primitives.Resource) (*manifold.Resource, error) {
	rs, err := c.GetResources(ctx, project, []*primitives.Resource{res})
	if err != nil {
		return nil, err
	}

	if len(rs) > 1 {
		// Labels are unique per project. If no project is specified it could be
		// that there are multiple resources.
		// TODO: figure out if we can setup a test for this.
		return nil, ErrMultipleResourcesFound
	}

	return rs[0], nil
}

// GetResourceCredentialValues is a wrapper function that knows how to get a set
// of specific credentials for a given requested resource.
func (c *Client) GetResourceCredentialValues(ctx context.Context, project *string, res *primitives.Resource) ([]*primitives.CredentialValue, error) {
	resourceCreds, err := c.GetResourcesCredentialValues(ctx, project, []*primitives.Resource{res})
	if err != nil {
		return nil, err
	}

	creds, ok := resourceCreds[res.Spec.Label]
	if !ok {
		return nil, ErrResourceNotFound
	}

	return creds, nil
}

// GetResourcesCredentialValues is a wrapper function that gets a list of
// CredentialValues for a list of resources and then maps all the credentials to
// it's specific Resource using the Resource Label.
// This also takes care of filling up the gaps. If you have requested a
// ResourceCredential with a non existing key but you've provided a Default
// value, it will be added to the list. If no default value is given, it will
// error.
func (c *Client) GetResourcesCredentialValues(ctx context.Context, project *string, res []*primitives.Resource) (map[string][]*primitives.CredentialValue, error) {
	for _, r := range res {
		if !r.Valid() {
			return nil, ErrResourceInvalid
		}
	}

	resources, err := c.GetResources(ctx, project, res)
	if err != nil {
		return nil, err
	}

	resourceIDs := make([]manifold.ID, len(resources))
	resourceLabels := map[manifold.ID]string{}
	for i, res := range resources {
		resourceIDs[i] = res.ID
		resourceLabels[res.ID] = res.Body.Label
	}

	credList := c.cl.Credentials.List(ctx, resourceIDs)
	defer credList.Close()

	resourceCredentials := map[string][]*primitives.CredentialValue{}
	for credList.Next() {
		cred, err := credList.Current()
		if err != nil {
			return nil, err
		}

		resourceCreds, ok := resourceCredentials[resourceLabels[cred.Body.ResourceID]]
		if !ok {
			resourceCreds = []*primitives.CredentialValue{}
		}

		for k, v := range cred.Body.Values {
			cv := &primitives.CredentialValue{
				CredentialSpec: primitives.CredentialSpec{
					Key: k,
				},
				Value: v,
			}

			err := setCredentialValueFields(cv, resourceLabels[cred.Body.ResourceID], res)
			switch err {
			case nil:
				resourceCreds = append(resourceCreds, cv)
			case ErrCredentialNotSpecified:
				// when the credential is not specified, it means that it
				// shouldn't be listed, skip from adding.
			default:
				return nil, err
			}
		}

		resourceCredentials[resourceLabels[cred.Body.ResourceID]] = resourceCreds
	}

	if err := fillDefaultCredentials(resourceCredentials, res); err != nil {
		return nil, err
	}

	return resourceCredentials, nil
}

func fillDefaultCredentials(rc map[string][]*primitives.CredentialValue, res []*primitives.Resource) error {
	for _, r := range res {
		// No credentials specified, skip it
		if len(r.Spec.Credentials) == 0 {
			continue
		}

		rcreds := rc[r.Spec.Label]
		for _, cred := range r.Spec.Credentials {
			var set bool

			for _, c := range rcreds {
				if c.Key == cred.Key {
					set = true
					break
				}
			}

			if !set {
				if cred.Default == "" {
					return ErrCredentialDefaultNotSet
				} else {
					cv := &primitives.CredentialValue{
						CredentialSpec: primitives.CredentialSpec{
							Key:  cred.Key,
							Name: cred.Name,
						},
						Value: cred.Default,
					}

					rcreds = append(rcreds, cv)
				}
			}
		}
		rc[r.Spec.Label] = rcreds
	}

	return nil
}

func setCredentialValueFields(cv *primitives.CredentialValue, label string, res []*primitives.Resource) error {
	for _, r := range res {
		// Not a label for this resource, skip it
		if label != r.Spec.Label {
			continue
		}

		// No credentials specified for this resource, skip it
		if len(r.Spec.Credentials) == 0 {
			return nil
		}

		for _, cred := range r.Spec.Credentials {
			if cred.Key == cv.Key {
				cv.Default = cred.Default
				cv.Name = cred.Name
				return nil
			}
		}
	}

	return ErrCredentialNotSpecified
}

// GetResources fetches a set of resources according to their labels. If no
// resources are given, all the resources will be fetched. If one of the
// requested resources is not available, this will return an error.
func (c *Client) GetResources(ctx context.Context, project *string, res []*primitives.Resource) ([]*manifold.Resource, error) {
	for _, r := range res {
		if !r.Valid() {
			return nil, ErrResourceInvalid
		}
	}

	pID, err := c.ProjectID(project)
	if err != nil {
		return nil, err
	}

	resourceList := c.cl.Resources.List(ctx, &manifold.ResourcesListOpts{
		ProjectID: pID,
		TeamID:    c.teamID,
	})
	defer resourceList.Close()

	resources := []*manifold.Resource{}
	for resourceList.Next() {
		resource, err := resourceList.Current()
		if err != nil {
			return nil, err
		}

		if requestedResource(resource, res) {
			resources = append(resources, resource)
		}
	}

	if len(resources) != len(res) {
		return nil, ErrResourceNotFound
	}

	return resources, nil
}

// ProjectID will return the ID for a project based on it's label. It uses an
// internal cache so it doesn't have to multiple requests for a single label.
func (c *Client) ProjectID(label *string) (*manifold.ID, error) {
	if label == nil {
		return nil, nil
	}

	if v, ok := c.getCachedProjectID(*label); ok {
		return v, nil
	}

	projectList := c.cl.Projects.List(context.Background(), &manifold.ProjectsListOpts{
		Label:  label,
		TeamID: c.teamID,
	})
	defer projectList.Close()

	for projectList.Next() {
		project, err := projectList.Current()
		if err != nil {
			return nil, err
		}

		if project.Body.Label == *label {
			c.setCachedProjectID(*label, &project.ID)
			return &project.ID, nil
		}
	}

	return nil, ErrProjectNotFound
}

// wrapper around the map to get a ReadLock for concurrency. This could easily
// be done with Go 1.9 sync.Map, but we want to support versions below.
func (c *Client) getCachedProjectID(label string) (*manifold.ID, bool) {
	c.RLock()
	defer c.RUnlock()

	v, ok := c.projectIDs[label]
	return v, ok
}

// wrapper around the map to get a Lock for concurrency. This could easily be
// done with Go 1.9 sync.Map, but we want to support versions below.
func (c *Client) setCachedProjectID(label string, id *manifold.ID) {
	c.Lock()
	defer c.Unlock()

	c.projectIDs[label] = id
}

func (c *Client) ensureTeamID() error {
	if c.team == nil {
		// no team specified, skip it
		return nil
	}

	teamsList := c.cl.Teams.List(context.Background())
	defer teamsList.Close()

	for teamsList.Next() {
		team, err := teamsList.Current()
		if err != nil {
			return err
		}

		if team.Body.Label == *c.team {
			c.teamID = &team.ID
			return nil
		}
	}

	return ErrTeamNotFound
}

func requestedResource(res *manifold.Resource, ress []*primitives.Resource) bool {
	if len(ress) == 0 {
		return true
	}

	for _, r := range ress {
		if res.Body.Label == r.Spec.Label {
			return true
		}
	}

	return false
}