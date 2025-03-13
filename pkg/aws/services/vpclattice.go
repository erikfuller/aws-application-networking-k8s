package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/vpclattice"
	vpclatticetypes "github.com/aws/aws-sdk-go-v2/service/vpclattice/types"
	"github.com/aws/smithy-go"

	"github.com/aws/aws-application-networking-k8s/pkg/config"
	"github.com/aws/aws-application-networking-k8s/pkg/utils"
)

//go:generate mockgen -destination vpclattice_mocks.go -package services github.com/aws/aws-application-networking-k8s/pkg/aws/services Lattice

var (
	ErrNameConflict = errors.New("name conflict")
	ErrNotFound     = errors.New("not found")
	ErrInternal     = errors.New("internal error")
)

const (
	ErrCodeResourceNotFoundException = "ResourceNotFoundException"
	ErrCodeAccessDeniedException     = "AccessDeniedException"
)

type ServiceNetworkInfo struct {
	SvcNetwork vpclatticetypes.ServiceNetworkSummary
	Tags       Tags
}

func NewNotFoundError(resourceType string, name string) error {
	return fmt.Errorf("%w, %s %s", ErrNotFound, resourceType, name)
}

func IsNotFoundError(err error) bool {
	if aerr, ok := err.(smithy.APIError); ok {
		if aerr.ErrorCode() == ErrCodeResourceNotFoundException {
			return true
		}
	}
	return errors.Is(err, ErrNotFound)
}

func IgnoreNotFound(err error) error {
	if IsNotFoundError(err) {
		return nil
	}
	return err
}

type ConflictError struct {
	ResourceType string
	Name         string
	Message      string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s %s had a conflict: %s", e.ResourceType, e.Name, e.Message)
}

func NewConflictError(resourceType string, name string, message string) error {
	return &ConflictError{resourceType, name, message}
}

func IsConflictError(err error) bool {
	conflictErr := &ConflictError{}
	return errors.As(err, &conflictErr)
}

type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("Invalid input: %s", e.Message)
}

func NewInvalidError(message string) error {
	return &InvalidError{message}
}

func IsInvalidError(err error) bool {
	invalidErr := &InvalidError{}
	return errors.As(err, &invalidErr)
}

type Lattice interface {
	Client() *vpclattice.Client
	ListListenersAsList(ctx context.Context, input *vpclattice.ListListenersInput) ([]vpclatticetypes.ListenerSummary, error)
	GetRulesAsList(ctx context.Context, input *vpclattice.ListRulesInput) ([]vpclattice.GetRuleOutput, error)
	ListRulesAsList(ctx context.Context, input *vpclattice.ListRulesInput) ([]vpclatticetypes.RuleSummary, error)
	ListServiceNetworksAsList(ctx context.Context, input *vpclattice.ListServiceNetworksInput) ([]vpclatticetypes.ServiceNetworkSummary, error)
	ListServicesAsList(ctx context.Context, input *vpclattice.ListServicesInput) ([]vpclatticetypes.ServiceSummary, error)
	// ListTagsForResource cached version, use instead of calling directly against Client()
	ListTagsForResource(ctx context.Context, input *vpclattice.ListTagsForResourceInput) (*vpclattice.ListTagsForResourceOutput, error)
	ListTargetGroupsAsList(ctx context.Context, input *vpclattice.ListTargetGroupsInput) ([]vpclatticetypes.TargetGroupSummary, error)
	ListTargetsAsList(ctx context.Context, input *vpclattice.ListTargetsInput) ([]vpclatticetypes.TargetSummary, error)
	ListServiceNetworkVpcAssociationsAsList(ctx context.Context, input *vpclattice.ListServiceNetworkVpcAssociationsInput) ([]vpclatticetypes.ServiceNetworkVpcAssociationSummary, error)
	ListServiceNetworkServiceAssociationsAsList(ctx context.Context, input *vpclattice.ListServiceNetworkServiceAssociationsInput) ([]vpclatticetypes.ServiceNetworkServiceAssociationSummary, error)
	FindServiceNetwork(ctx context.Context, nameOrId string) (*ServiceNetworkInfo, error)
	FindService(ctx context.Context, latticeServiceName string) (*vpclatticetypes.ServiceSummary, error)
	// TagResource cached version, use instead of calling directly against Client()
	TagResource(ctx context.Context, input *vpclattice.TagResourceInput) (*vpclattice.TagResourceOutput, error)
}

type defaultLattice struct {
	client     *vpclattice.Client
	ownAccount string
	cache      *expirable.LRU[string, any]
}

func (d *defaultLattice) Client() *vpclattice.Client {
	return d.client
}

func NewDefaultLattice(baseCfg aws.Config, acc string, region string) (*defaultLattice, error) {

	latticeEndpoint := "https://vpc-lattice." + region + ".amazonaws.com"
	endpoint := os.Getenv("LATTICE_ENDPOINT")

	if endpoint == "" {
		endpoint = latticeEndpoint
	}

	cfg := baseCfg.Copy()
	cfg.Region = region
	cfg.BaseEndpoint = &endpoint
	cfg.RetryMaxAttempts = 2

	client := vpclattice.NewFromConfig(cfg, func(o *vpclattice.Options) {
		o.TracerProvider = nil
		o.MeterProvider = nil
	})
	cache := expirable.NewLRU[string, any](1000, nil, time.Second*60)

	return &defaultLattice{
		client:     client,
		ownAccount: acc,
		cache:      cache,
	}, nil
}

func (d *defaultLattice) ListListenersAsList(ctx context.Context, input *vpclattice.ListListenersInput) ([]vpclatticetypes.ListenerSummary, error) {
	var result []vpclatticetypes.ListenerSummary

	paginator := vpclattice.NewListListenersPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) GetRulesAsList(ctx context.Context, input *vpclattice.ListRulesInput) ([]vpclattice.GetRuleOutput, error) {
	var result []vpclattice.GetRuleOutput

	paginator := vpclattice.NewListRulesPaginator(d.Client(), input)
	var innerErr error
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range output.Items {
			grInput := vpclattice.GetRuleInput{
				ServiceIdentifier:  input.ServiceIdentifier,
				ListenerIdentifier: input.ListenerIdentifier,
				RuleIdentifier:     r.Id,
			}

			var gro *vpclattice.GetRuleOutput
			gro, innerErr = d.Client().GetRule(ctx, &grInput)
			if innerErr != nil {
				return nil, innerErr
			}
			result = append(result, *gro)
		}
	}

	if innerErr != nil {
		return nil, innerErr
	}

	return result, nil
}

func (d *defaultLattice) ListRulesAsList(ctx context.Context, input *vpclattice.ListRulesInput) ([]vpclatticetypes.RuleSummary, error) {
	var result []vpclatticetypes.RuleSummary

	paginator := vpclattice.NewListRulesPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListServiceNetworksAsList(ctx context.Context, input *vpclattice.ListServiceNetworksInput) ([]vpclatticetypes.ServiceNetworkSummary, error) {
	var result []vpclatticetypes.ServiceNetworkSummary

	paginator := vpclattice.NewListServiceNetworksPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListServicesAsList(ctx context.Context, input *vpclattice.ListServicesInput) ([]vpclatticetypes.ServiceSummary, error) {
	var result []vpclatticetypes.ServiceSummary
	paginator := vpclattice.NewListServicesPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListTargetGroupsAsList(ctx context.Context, input *vpclattice.ListTargetGroupsInput) ([]vpclatticetypes.TargetGroupSummary, error) {
	var result []vpclatticetypes.TargetGroupSummary
	paginator := vpclattice.NewListTargetGroupsPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListTagsForResource(ctx context.Context, input *vpclattice.ListTagsForResourceInput) (*vpclattice.ListTagsForResourceOutput, error) {
	key := tagCacheKey(*input.ResourceArn)
	if d.cache != nil {
		r, ok := d.cache.Get(key)
		if ok {
			return r.(*vpclattice.ListTagsForResourceOutput), nil
		}
	}

	out, err := d.Client().ListTagsForResource(ctx, input)
	if err != nil {
		return nil, err
	}
	if d.cache != nil {
		d.cache.Add(key, out)
	}
	return out, nil
}

func tagCacheKey(arn string) string {
	return "tag-" + arn
}

func (d *defaultLattice) TagResource(ctx context.Context, input *vpclattice.TagResourceInput) (*vpclattice.TagResourceOutput, error) {
	if d.cache != nil {
		key := tagCacheKey(*input.ResourceArn)
		d.cache.Remove(key)
	}
	return d.Client().TagResource(ctx, input)
}

func (d *defaultLattice) ListTargetsAsList(ctx context.Context, input *vpclattice.ListTargetsInput) ([]vpclatticetypes.TargetSummary, error) {
	var result []vpclatticetypes.TargetSummary
	paginator := vpclattice.NewListTargetsPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListServiceNetworkVpcAssociationsAsList(ctx context.Context, input *vpclattice.ListServiceNetworkVpcAssociationsInput) ([]vpclatticetypes.ServiceNetworkVpcAssociationSummary, error) {
	var result []vpclatticetypes.ServiceNetworkVpcAssociationSummary
	paginator := vpclattice.NewListServiceNetworkVpcAssociationsPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) ListServiceNetworkServiceAssociationsAsList(ctx context.Context, input *vpclattice.ListServiceNetworkServiceAssociationsInput) ([]vpclatticetypes.ServiceNetworkServiceAssociationSummary, error) {
	var result []vpclatticetypes.ServiceNetworkServiceAssociationSummary
	paginator := vpclattice.NewListServiceNetworkServiceAssociationsPaginator(d.Client(), input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, output.Items...)
	}

	return result, nil
}

func (d *defaultLattice) snSummaryToLog(snSum []vpclatticetypes.ServiceNetworkSummary) string {
	out := make([]string, len(snSum))
	for i, s := range snSum {
		out[i] = fmt.Sprintf("{name=%s, id=%s}", aws.ToString(s.Name), aws.ToString(s.Id))
	}
	return strings.Join(out, ",")
}

// Try find by name first, if there is no single match, continue with id match. Ideally name match
// should work just fine, but in desperate scenario of shared SN when naming collision happens using
// id can be an option
func (d *defaultLattice) serviceNetworkMatch(allSn []vpclatticetypes.ServiceNetworkSummary, nameOrId string) (*vpclatticetypes.ServiceNetworkSummary, error) {
	var snMatch *vpclatticetypes.ServiceNetworkSummary
	nameMatch := utils.SliceFilter(allSn, func(snSum vpclatticetypes.ServiceNetworkSummary) bool {
		return aws.ToString(snSum.Name) == nameOrId
	})
	idMatch := utils.SliceFilter(allSn, func(snSum vpclatticetypes.ServiceNetworkSummary) bool {
		return aws.ToString(snSum.Id) == nameOrId
	})

	switch {
	case len(nameMatch) == 0 && len(idMatch) == 0:
		return nil, NewNotFoundError("Service network", nameOrId)
	case len(nameMatch)+len(idMatch) > 1:
		return nil, fmt.Errorf("%w, multiple SN found: nameMatch=%s idMatch=%s",
			ErrNameConflict, d.snSummaryToLog(nameMatch), d.snSummaryToLog(idMatch))
	case len(nameMatch) == 1:
		snMatch = &nameMatch[0]
	case len(idMatch) == 1:
		snMatch = &idMatch[0]
	default:
		return nil, fmt.Errorf("%w: service network match: unreachable", ErrInternal)
	}
	return snMatch, nil
}

// checks if given string ARN belongs to given account
func (d *defaultLattice) isLocalResource(strArn string) (bool, error) {
	a, err := arn.Parse(strArn)
	if err != nil {
		return false, err
	}
	return a.AccountID == d.ownAccount || d.ownAccount == "", nil
}

func (d *defaultLattice) FindServiceNetwork(ctx context.Context, nameOrId string) (*ServiceNetworkInfo, error) {
	// When default service network is provided, override for any kind of SN search
	if config.ServiceNetworkOverrideMode {
		nameOrId = config.DefaultServiceNetwork
	}

	input := &vpclattice.ListServiceNetworksInput{}
	allSn, err := d.ListServiceNetworksAsList(ctx, input)
	if err != nil {
		return nil, err
	}

	snMatch, err := d.serviceNetworkMatch(allSn, nameOrId)
	if err != nil {
		return nil, err
	}

	// try to fetch tags only if SN in the same aws account with controller's config
	tags := Tags{}
	isLocal, err := d.isLocalResource(aws.ToString(snMatch.Arn))
	if err != nil {
		return nil, err
	}
	if isLocal {
		tagsInput := vpclattice.ListTagsForResourceInput{ResourceArn: snMatch.Arn}
		tagsOutput, err := d.ListTagsForResource(ctx, &tagsInput)
		if err != nil {
			aerr, ok := err.(smithy.APIError)
			// In case ownAccount is not set, we cant tell if SN is foreign.
			// In this case access denied is expected.
			if !ok || aerr.ErrorCode() != ErrCodeAccessDeniedException {
				return nil, err
			}
		} else {
			tags = tagsOutput.Tags
		}
	}

	return &ServiceNetworkInfo{
		SvcNetwork: *snMatch,
		Tags:       tags,
	}, nil
}

// see utils.LatticeServiceName
func (d *defaultLattice) FindService(ctx context.Context, latticeServiceName string) (*vpclatticetypes.ServiceSummary, error) {
	input := vpclattice.ListServicesInput{}

	var svcMatch *vpclatticetypes.ServiceSummary
	paginator := vpclattice.NewListServicesPaginator(d.Client(), &input)
	for (svcMatch == nil) && paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, svc := range output.Items {
			if *svc.Name == latticeServiceName {
				svcMatch = &svc
				break
			}
		}
	}

	if svcMatch == nil {
		return nil, NewNotFoundError("Service", latticeServiceName)
	}

	return svcMatch, nil
}

func IsLatticeAPINotFoundErr(err error) bool {
	if err == nil {
		return false
	}

	var aErr smithy.APIError
	if errors.As(err, &aErr) {
		return aErr.ErrorCode() == ErrCodeResourceNotFoundException
	}
	return false
}
