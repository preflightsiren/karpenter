/*
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

package placementgroup

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/patrickmn/go-cache"

	"github.com/aws/karpenter/pkg/apis/v1beta1"

	"github.com/aws/karpenter-core/pkg/utils/pretty"
	"knative.dev/pkg/logging"
)

type Provider struct {
	sync.RWMutex
	ec2api ec2iface.EC2API
	cache  *cache.Cache
	cm     *pretty.ChangeMonitor
}

func NewProvider(ec2api ec2iface.EC2API, cache *cache.Cache) *Provider {
	return &Provider{
		ec2api: ec2api,
		cm:     pretty.NewChangeMonitor(),
		// TODO: Remove cache for v1beta1, utilize resolved subnet from the AWSNodeTemplate.status
		// Subnets are sorted on AvailableIpAddressCount, descending order
		cache: cache,
	}
}

func (p *Provider) Get(ctx context.Context, nodeClass *v1beta1.EC2NodeClass) (*ec2.PlacementGroup, error) {
	p.Lock()
	defer p.Unlock()

	// Get selectors from the nodeClass, exit if no selectors defined
	selectors := nodeClass.Spec.LicenseSelectorTerms
	if selectors == nil {
		return nil, nil
	}

	// Look for a cached result
	hash, err := hashstructure.Hash(selectors, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	if err != nil {
		return nil, err
	}
	if cached, ok := p.cache.Get(fmt.Sprint(hash)); ok {
		return cached.(*ec2.PlacementGroup), nil
	}

	var group *ec2.PlacementGroup
	// Look up all License Configurations
	output, err := p.ec2api.DescribePlacementGroupsWithContext(ctx, &ec2.DescribePlacementGroupsInput{})
	if err != nil {
		logging.FromContext(ctx).
			With("aws error", err).
			Debugf("Error from ec2:describeplacementgroups")
		return nil, err
	}
	for i := range output.PlacementGroups {
		// filter results to only include those that match at least 1 selector
		for x := range selectors {
			if *output.PlacementGroups[i].GroupName == selectors[x].Name {
				group = output.PlacementGroups[i]
				break
			}
		}
	}

	if p.cm.HasChanged(fmt.Sprintf("placementGroups/%t/%s", nodeClass.IsNodeTemplate, nodeClass.Name), group) {
		logging.FromContext(ctx).
			With("placementGroup", group).
			Debugf("discovered placement groups")
	}

	return group, nil
}
