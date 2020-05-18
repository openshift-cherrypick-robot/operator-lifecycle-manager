//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -o fakes/fake_registry_client.go ../../../../vendor/github.com/operator-framework/operator-registry/pkg/api.RegistryClient
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -o fakes/fake_registry_interface.go ../../../../vendor/github.com/operator-framework/operator-registry/pkg/client/client.go Interface
package resolver

import (
	"context"
	"fmt"

	"github.com/blang/semver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/operator-framework/operator-registry/pkg/api"
	registryapi "github.com/operator-framework/operator-registry/pkg/api"
	"github.com/operator-framework/operator-registry/pkg/client"
	opregistry "github.com/operator-framework/operator-registry/pkg/registry"

	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry"
)

const SkipPackageAnnotationKey = "olm.skipRange"

type SourceRef struct {
	Address     string
	Client      client.Interface
	LastConnect metav1.Time
	LastHealthy metav1.Time
}

type SourceQuerier interface {
	FindProvider(api opregistry.APIKey, initialSource CatalogKey, pkgName string) (*api.Bundle, *CatalogKey, error)
	FindBundle(pkgName, channelName, bundleName string, initialSource CatalogKey) (*api.Bundle, *CatalogKey, error)
	FindLatestBundle(pkgName, channelName string, initialSource CatalogKey) (*api.Bundle, *CatalogKey, error)
	FindReplacement(currentVersion *semver.Version, bundleName, pkgName, channelName string, initialSource CatalogKey) (*api.Bundle, *CatalogKey, error)
	Queryable() error
}

type NamespaceSourceQuerier struct {
	sources map[CatalogKey]client.Interface
	clients map[CatalogKey]registry.RegistryClientInterface
}

var _ SourceQuerier = &NamespaceSourceQuerier{}

func NewNamespaceSourceQuerier(sources map[CatalogKey]client.Interface, clients map[CatalogKey]registry.RegistryClientInterface) *NamespaceSourceQuerier {
	return &NamespaceSourceQuerier{
		sources: sources,
		clients: clients,
	}
}

func (q *NamespaceSourceQuerier) Queryable() error {
	if len(q.sources) == 0 {
		return fmt.Errorf("no catalog sources available")
	}
	return nil
}

func (q *NamespaceSourceQuerier) FindProvider(api opregistry.APIKey, initialSource CatalogKey, pkgName string) (*registryapi.Bundle, *CatalogKey, error) {
	if initialSource.Name != "" && initialSource.Namespace != "" {
		client, ok := q.clients[initialSource]
		if ok {
			if bundle, err := client.FindBundleThatProvides(context.TODO(), api.Group, api.Version, api.Kind, pkgName); err == nil {
				return bundle, &initialSource, nil
			}
			if bundle, err := client.FindBundleThatProvides(context.TODO(), api.Plural+"."+api.Group, api.Version, api.Kind, pkgName); err == nil {
				return bundle, &initialSource, nil
			}
		}
	}
	for key, client := range q.clients {
		if bundle, err := client.FindBundleThatProvides(context.TODO(), api.Group, api.Version, api.Kind, pkgName); err == nil {
			return bundle, &key, nil
		}
		if bundle, err := client.FindBundleThatProvides(context.TODO(), api.Plural+"."+api.Group, api.Version, api.Kind, pkgName); err == nil {
			return bundle, &key, nil
		}
	}
	return nil, nil, fmt.Errorf("%s not provided by a package in any CatalogSource", api)
}

func (q *NamespaceSourceQuerier) FindBundle(pkgName, channelName, bundleName string, initialSource CatalogKey) (*opregistry.Bundle, *CatalogKey, error) {
	if initialSource.Name != "" && initialSource.Namespace != "" {
		source, ok := q.sources[initialSource]
		if !ok {
			return nil, nil, fmt.Errorf("CatalogSource %s not found", initialSource)
		}

		bundle, err := source.GetBundle(context.TODO(), pkgName, channelName, bundleName)
		if err != nil {
			return nil, nil, err
		}
		return bundle, &initialSource, nil
	}

	for key, source := range q.sources {
		bundle, err := source.GetBundle(context.TODO(), pkgName, channelName, bundleName)
		if err == nil {
			return bundle, &key, nil
		}
	}
	return nil, nil, fmt.Errorf("%s/%s/%s not found in any available CatalogSource", pkgName, channelName, bundleName)
}

func (q *NamespaceSourceQuerier) FindLatestBundle(pkgName, channelName string, initialSource CatalogKey) (*opregistry.Bundle, *CatalogKey, error) {
	if initialSource.Name != "" && initialSource.Namespace != "" {
		source, ok := q.sources[initialSource]
		if !ok {
			return nil, nil, fmt.Errorf("CatalogSource %s not found", initialSource)
		}

		bundle, err := source.GetBundleInPackageChannel(context.TODO(), pkgName, channelName)
		if err != nil {
			return nil, nil, err
		}
		return bundle, &initialSource, nil
	}

	for key, source := range q.sources {
		bundle, err := source.GetBundleInPackageChannel(context.TODO(), pkgName, channelName)
		if err == nil {
			return bundle, &key, nil
		}
	}
	return nil, nil, fmt.Errorf("%s/%s not found in any available CatalogSource", pkgName, channelName)
}

func (q *NamespaceSourceQuerier) FindReplacement(currentVersion *semver.Version, bundleName, pkgName, channelName string, initialSource CatalogKey) (*opregistry.Bundle, *CatalogKey, error) {
	errs := []error{}

	if initialSource.Name != "" && initialSource.Namespace != "" {
		source, ok := q.sources[initialSource]
		if !ok {
			return nil, nil, fmt.Errorf("CatalogSource %s not found", initialSource.Name)
		}

		bundle, err := q.findChannelHead(currentVersion, pkgName, channelName, source)
		if bundle != nil {
			return bundle, &initialSource, nil
		}
		if err != nil {
			errs = append(errs, err)
		}

		bundle, err = source.GetReplacementBundleInPackageChannel(context.TODO(), bundleName, pkgName, channelName)
		if bundle != nil {
			return bundle, &initialSource, nil
		}
		if err != nil {
			errs = append(errs, err)
		}

		return nil, nil, errors.NewAggregate(errs)
	}

	for key, source := range q.sources {
		bundle, err := q.findChannelHead(currentVersion, pkgName, channelName, source)
		if bundle != nil {
			return bundle, &initialSource, nil
		}
		if err != nil {
			errs = append(errs, err)
		}

		bundle, err = source.GetReplacementBundleInPackageChannel(context.TODO(), bundleName, pkgName, channelName)
		if bundle != nil {
			return bundle, &key, nil
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return nil, nil, errors.NewAggregate(errs)
}

func (q *NamespaceSourceQuerier) findChannelHead(currentVersion *semver.Version, pkgName, channelName string, source client.Interface) (*opregistry.Bundle, error) {
	if currentVersion == nil {
		return nil, nil
	}

	latest, err := source.GetBundleInPackageChannel(context.TODO(), pkgName, channelName)
	if err != nil {
		return nil, err
	}

	csv, err := latest.ClusterServiceVersion()
	if err != nil {
		return nil, err
	}
	if csv == nil {
		return nil, nil
	}

	skipRange, ok := csv.GetAnnotations()[SkipPackageAnnotationKey]
	if !ok {
		return nil, nil
	}

	r, err := semver.ParseRange(skipRange)
	if err != nil {
		return nil, err
	}

	if r(*currentVersion) {
		return latest, nil
	}
	return nil, nil
}
