package master

import (
	"fmt"

	"github.com/pulumi/pulumi-equinix-metal/sdk/go/equinix"
	"github.com/pulumi/pulumi-ns1/sdk/go/ns1"
	"github.com/pulumi/pulumi-random/sdk/v3/go/random"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi/config"
	"github.com/tinkerbell/infrastructure/src/internal"
)

// SaltMasterConfig is the struct we allow in the stack configuration
// to describe the SaltMaster we provision.
type SaltMasterConfig struct {
	Facility equinix.Facility
	Plan     equinix.Plan
}

// SaltMaster is the return struct for CreateSaltMaster.
type SaltMaster struct {
	Device equinix.Device
}

type TeleportConfig struct {
	ClientID     string
	ClientSecret string
	PeerToken    *random.RandomUuid
}

type GitHubConfig struct {
	Username    string
	AccessToken string
}

type AwsConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	BucketLocation  string
}

// CreateSaltMaster Provisions a SaltMaster.
func CreateSaltMaster(ctx *pulumi.Context, infrastructure internal.Infrastructure) (SaltMaster, error) {
	metalConfig := config.New(ctx, "equinix-metal")
	projectID := metalConfig.Require("projectId")

	stackConfig := config.New(ctx, "")
	saltMasterConfig := &SaltMasterConfig{}
	stackConfig.RequireObject("saltMaster", saltMasterConfig)

	elasticIP, err := equinix.NewReservedIpBlock(ctx, "salt-master", &equinix.ReservedIpBlockArgs{
		Facility:  saltMasterConfig.Facility,
		ProjectId: pulumi.String(projectID),
		Quantity:  pulumi.Int(1),
	})
	if err != nil {
		return SaltMaster{}, err
	}

	var teleportConfig TeleportConfig
	stackConfig.RequireObject("teleport", &teleportConfig)
	teleportDomain := fmt.Sprintf("teleport.%s", infrastructure.Zone.Zone)

	// Generate random PeerToken for Teleport cluster
	peerToken, err := random.NewRandomUuid(ctx, "teleport-peer-token", nil)
	if err != nil {
		return SaltMaster{}, err
	}

	teleportConfig.PeerToken = peerToken

	var gitHubConfig GitHubConfig
	stackConfig.RequireObject("github", &gitHubConfig)

	var awsConfig AwsConfig
	stackConfig.RequireObject("aws", &awsConfig)

	bootstrapConfig := &BootstrapConfig{
		teleportDomain:       teleportDomain,
		teleportClientID:     teleportConfig.ClientID,
		teleportClientSecret: teleportConfig.ClientSecret,
		githubUsername:       gitHubConfig.Username,
		githubAccessToken:    gitHubConfig.AccessToken,
		awsBucketName:        awsConfig.BucketName,
		awsBucketLocation:    awsConfig.BucketLocation,
		awsAccessKeyID:       awsConfig.AccessKeyID,
		awsSecretAccessKey:   awsConfig.SecretAccessKey,
	}

	deviceArgs := equinix.DeviceArgs{
		ProjectId: pulumi.String(projectID),
		Hostname:  pulumi.String(fmt.Sprintf("%s-%s", ctx.Stack(), "salt-master")),
		Plan:      saltMasterConfig.Plan,
		Facilities: pulumi.StringArray{
			saltMasterConfig.Facility,
		},
		OperatingSystem: equinix.OperatingSystemUbuntu2004,
		Tags: pulumi.StringArray{
			pulumi.String("role:salt-master"),
		},
		BillingCycle: equinix.BillingCycleHourly,
		UserData: teleportConfig.PeerToken.Result.ApplyString(func(s string) string {
			bootstrapConfig.teleportPeerToken = s
			return cloudInitConfig(bootstrapConfig)
		}),
	}

	device, err := equinix.NewDevice(ctx, "salt-master", &deviceArgs)
	if err != nil {
		return SaltMaster{}, err
	}

	ctx.Export("saltMasterEip", elasticIP.Address)
	ctx.Export("saltMasterIp", &device.AccessPublicIpv4)

	if err != nil {
		return SaltMaster{}, err
	}

	_, err = equinix.NewIpAttachment(ctx, "salt-master", &equinix.IpAttachmentArgs{
		DeviceId:     device.ID(),
		CidrNotation: elasticIP.CidrNotation,
	}, pulumi.DeleteBeforeReplace(true))

	if err != nil {
		return SaltMaster{}, err
	}

	// Create DNS record for Teleport
	_, err = ns1.NewRecord(ctx, "teleport", &ns1.RecordArgs{
		Zone:   pulumi.String(infrastructure.Zone.Zone),
		Domain: pulumi.String(teleportDomain),
		Type:   pulumi.String("A"),
		Answers: ns1.RecordAnswerArray{
			ns1.RecordAnswerArgs{
				Answer: elasticIP.Address,
			},
		},
	})

	if err != nil {
		return SaltMaster{}, err
	}

	return SaltMaster{
		Device: *device,
	}, nil
}
