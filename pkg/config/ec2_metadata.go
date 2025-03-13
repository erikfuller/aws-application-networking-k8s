package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

type EC2Metadata interface {
	Region() (string, error)
	VpcID() (string, error)
	AccountId() (string, error)
}

// NewEC2Metadata constructs new EC2Metadata implementation.
func NewEC2Metadata(config aws.Config) EC2Metadata {
	return &defaultEC2Metadata{
		EC2Metadata: imds.NewFromConfig(config),
	}
}

type defaultEC2Metadata struct {
	EC2Metadata *imds.Client
}

func (c *defaultEC2Metadata) GetMetadata(path string) (string, error) {
	output, err := c.EC2Metadata.GetMetadata(context.TODO(), &imds.GetMetadataInput{
		Path: path,
	})
	if err != nil {
		return "", err
	}
	content, _ := io.ReadAll(output.Content)
	return string(content), nil
}

func (c *defaultEC2Metadata) VpcID() (string, error) {
	mac, err := c.GetMetadata("mac")
	if err != nil {
		return "", err
	}
	vpcID, err := c.GetMetadata(fmt.Sprintf("network/interfaces/macs/%s/vpc-id", mac))
	if err != nil {
		return "", err
	}
	return vpcID, nil
}

func (c *defaultEC2Metadata) Region() (string, error) {
	region, err := c.GetMetadata("placement/region")
	if err != nil {
		return "", err
	}
	return region, nil
}

func (c *defaultEC2Metadata) AccountId() (string, error) {
	ec2Info, err := c.GetMetadata("identity-credentials/ec2/info")
	type accountInfo struct {
		Code        string `json:"code"`
		LastUpdated string `json:"LastUpdated"`
		AccountId   string `json:"AccountId"`
	}

	var acc accountInfo
	json.Unmarshal([]byte(ec2Info), &acc)
	if err != nil {
		return "", err
	}
	return acc.AccountId, nil
}
