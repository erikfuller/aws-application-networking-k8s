package services

import (
	"context"
	"fmt"
	"github.com/aws/aws-application-networking-k8s/pkg/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	rgtagapi "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtagapitypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/vpclattice"
)

//go:generate mockgen -destination tagging_mocks.go -package services github.com/aws/aws-application-networking-k8s/pkg/aws/services Tagging

type ResourceType string

const (
	resourceTypePrefix = "vpc-lattice:"

	ResourceTypeTargetGroup ResourceType = resourceTypePrefix + "targetgroup"
	ResourceTypeService     ResourceType = resourceTypePrefix + "service"

	// https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/API_GetResources.html#API_GetResources_RequestSyntax
	maxArnsPerGetResourcesApi = 100
)

type Tags = map[string]string

type Tagging interface {
	// Receives a list of arns and returns arn-to-tags map.
	GetTagsForArns(ctx context.Context, arns []string) (map[string]Tags, error)

	// Finds one resource that matches the given set of tags.
	FindResourcesByTags(ctx context.Context, resourceType ResourceType, tags Tags) ([]string, error)
}

type defaultTagging struct {
	client *rgtagapi.Client
}

type latticeTagging struct {
	lattice Lattice
	vpcId   string
}

func (t *defaultTagging) GetTagsForArns(ctx context.Context, arns []string) (map[string]Tags, error) {
	chunks := utils.Chunks(arns, maxArnsPerGetResourcesApi)
	result := make(map[string]Tags)

	for _, chunk := range chunks {
		input := &rgtagapi.GetResourcesInput{
			ResourceARNList: chunk,
		}

		paginator := rgtagapi.NewGetResourcesPaginator(t.client, input)
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			for _, r := range page.ResourceTagMappingList {
				result[*r.ResourceARN] = convertTags(r.Tags)
			}
		}
	}
	return result, nil
}

func (t *defaultTagging) FindResourcesByTags(ctx context.Context, resourceType ResourceType, tags Tags) ([]string, error) {
	input := &rgtagapi.GetResourcesInput{
		TagFilters:          convertTagsToFilter(tags),
		ResourceTypeFilters: []string{string(resourceType)},
	}
	resp, err := t.client.GetResources(ctx, input)
	if err != nil {
		return nil, err
	}
	matchingArns := utils.SliceMap(resp.ResourceTagMappingList, func(t rgtagapitypes.ResourceTagMapping) string {
		return aws.ToString(t.ResourceARN)
	})
	return matchingArns, nil
}

func NewDefaultTagging(baseCfg aws.Config, region string) *defaultTagging {
	cfg := baseCfg.Copy()
	cfg.Region = region

	api := rgtagapi.NewFromConfig(cfg)
	return &defaultTagging{client: api}
}

// Use VPC Lattice API instead of the Resource Groups Tagging API
func NewLatticeTagging(baseCfg aws.Config, acc string, region string, vpcId string) (*latticeTagging, error) {
	api, err := NewDefaultLattice(baseCfg, acc, region)
	if err != nil {
		return nil, err
	}
	return &latticeTagging{lattice: api, vpcId: vpcId}, nil
}

func (t *latticeTagging) GetTagsForArns(ctx context.Context, arns []string) (map[string]Tags, error) {
	result := map[string]Tags{}

	for _, arn := range arns {
		tags, err := t.lattice.ListTagsForResource(ctx,
			&vpclattice.ListTagsForResourceInput{ResourceArn: aws.String(arn)},
		)
		if err != nil {
			return nil, err
		}
		result[arn] = tags.Tags
	}
	return result, nil
}

func (t *latticeTagging) FindResourcesByTags(ctx context.Context, resourceType ResourceType, tags Tags) ([]string, error) {
	if resourceType != ResourceTypeTargetGroup {
		return nil, fmt.Errorf("unsupported resource type %q for FindResourcesByTags", resourceType)
	}

	tgs, err := t.lattice.ListTargetGroupsAsList(ctx, &vpclattice.ListTargetGroupsInput{
		VpcIdentifier: aws.String(t.vpcId),
	})
	if err != nil {
		return nil, err
	}

	arns := make([]string, 0, len(tgs))

	for _, tg := range tgs {
		resp, err := t.lattice.ListTagsForResource(ctx,
			&vpclattice.ListTagsForResourceInput{ResourceArn: tg.Arn},
		)
		if err != nil {
			return nil, err
		}

		if containsTags(resp.Tags, tags) {
			arns = append(arns, aws.ToString(tg.Arn))
		}
	}

	return arns, nil
}

func containsTags(source, check Tags) bool {
	for k, v := range check {
		sourceV, ok := source[k]
		if !ok || (sourceV != v) {
			return false
		}
	}
	return len(check) != 0
}

func convertTags(tags []rgtagapitypes.Tag) Tags {
	out := make(Tags)
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

func convertTagsToFilter(tags Tags) []rgtagapitypes.TagFilter {
	filters := make([]rgtagapitypes.TagFilter, 0, len(tags))
	for k, v := range tags {
		filters = append(filters, rgtagapitypes.TagFilter{
			Key:    aws.String(k),
			Values: []string{v},
		})
	}
	return filters
}
