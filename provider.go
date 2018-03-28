package tfextras

import (
	"log"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"

	tfaws "github.com/terraform-providers/terraform-provider-aws/aws"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/directconnect"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/aws/aws-sdk-go/service/sts"

	"github.com/hashicorp/go-cleanhttp"

	"crypto/tls"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"net/http"
	"strings"

	"github.com/hashicorp/errwrap"
)

var PREFIX string = "extras_"

type AWSClient struct {
	tfaws.AWSClient

	region      string
	partition   string
	accountid   string
	subProvider string

	r53conn *route53.Route53
	iamconn *iam.IAM
	stsconn *sts.STS
	dxconn  *directconnect.DirectConnect
}

var providers map[string]*AWSClient = map[string]*AWSClient{}
var awsProviderConfigure func(d *schema.ResourceData) (interface{}, error)

// Provider returns a terraform.ResourceProvider.
// It will additionally create instance of AWSClient from terraform builtin aws provider,
// which allows to re-use some of existing code from terraform.
func Provider() terraform.ResourceProvider {
	awsprovider := tfaws.Provider().(*schema.Provider)

	s := awsprovider.Schema

	s["sub_provider"] = &schema.Schema{
		Type:        schema.TypeString,
		Optional:    true,
		Default:     "",
		Description: "provider name, can be refered for specific cross-account resources",
	}

	awsProviderConfigure = awsprovider.ConfigureFunc

	return &schema.Provider{
		Schema:        s,
		ConfigureFunc: providerConfigure,
		ResourcesMap: map[string]*schema.Resource{
			PREFIX + "aws_route53_zone_association_authorization":    resourceAwsRoute53ZoneAssociationAuthorization(),
			PREFIX + "aws_route53_zone_association":                  resourceAwsRoute53ZoneAssociation(awsprovider),
			PREFIX + "aws_dx_private_virtual_interface":              resourceAwsDxPrivateVirtualInterface(),
			PREFIX + "aws_dx_private_virtual_interface_confirmation": resourceAwsDxPrivateVirtualInterfaceConfirmation(),
		},
	}
}

func providerConfigure(d *schema.ResourceData) (interface{}, error) {
	config := tfaws.Config{
		AccessKey:               d.Get("access_key").(string),
		SecretKey:               d.Get("secret_key").(string),
		Profile:                 d.Get("profile").(string),
		CredsFilename:           d.Get("shared_credentials_file").(string),
		Token:                   d.Get("token").(string),
		Region:                  d.Get("region").(string),
		MaxRetries:              d.Get("max_retries").(int),
		Insecure:                d.Get("insecure").(bool),
		SkipCredsValidation:     d.Get("skip_credentials_validation").(bool),
		SkipRegionValidation:    d.Get("skip_region_validation").(bool),
		SkipRequestingAccountId: d.Get("skip_requesting_account_id").(bool),
		SkipMetadataApiCheck:    d.Get("skip_metadata_api_check").(bool),
		S3ForcePathStyle:        d.Get("s3_force_path_style").(bool),
	}

	assumeRoleList := d.Get("assume_role").(*schema.Set).List()
	if len(assumeRoleList) == 1 {
		assumeRole := assumeRoleList[0].(map[string]interface{})
		config.AssumeRoleARN = assumeRole["role_arn"].(string)
		config.AssumeRoleSessionName = assumeRole["session_name"].(string)
		config.AssumeRoleExternalID = assumeRole["external_id"].(string)

		if v := assumeRole["policy"].(string); v != "" {
			config.AssumeRolePolicy = v
		}

		log.Printf("[INFO] assume_role configuration set: (ARN: %q, SessionID: %q, ExternalID: %q, Policy: %q)",
			config.AssumeRoleARN, config.AssumeRoleSessionName, config.AssumeRoleExternalID, config.AssumeRolePolicy)
	} else {
		log.Printf("[INFO] No assume_role block read from configuration")
	}

	endpointsSet := d.Get("endpoints").(*schema.Set)

	for _, endpointsSetI := range endpointsSet.List() {
		endpoints := endpointsSetI.(map[string]interface{})
		config.DynamoDBEndpoint = endpoints["dynamodb"].(string)
		config.IamEndpoint = endpoints["iam"].(string)
		config.Ec2Endpoint = endpoints["ec2"].(string)
		config.ElbEndpoint = endpoints["elb"].(string)
		config.KinesisEndpoint = endpoints["kinesis"].(string)
		config.S3Endpoint = endpoints["s3"].(string)
	}

	if v, ok := d.GetOk("allowed_account_ids"); ok {
		config.AllowedAccountIds = v.(*schema.Set).List()
	}

	if v, ok := d.GetOk("forbidden_account_ids"); ok {
		config.ForbiddenAccountIds = v.(*schema.Set).List()
	}

	var subProvider string
	if v, ok := d.GetOk("sub_provider"); ok {
		subProvider = v.(string)
	}

	awscl, err := awsProviderConfigure(d)

	if err != nil {
		return nil, err
	}

	cl := &AWSClient{AWSClient: *(awscl.(*tfaws.AWSClient))}

	return getClient(&config, subProvider, cl)
}

func getClientByName(subProvider string) *AWSClient {
	return providers[subProvider]
}

func getClient(c *tfaws.Config, subProvider string, client *AWSClient) (interface{}, error) {
	// Get the auth and region. This can fail if keys/regions were not
	// specified and we're attempting to use the environment.
	if c.SkipRegionValidation {
		log.Println("[INFO] Skipping region validation")
	} else {
		log.Println("[INFO] Building AWS region structure")
		err := c.ValidateRegion()
		if err != nil {
			return nil, err
		}
	}

	// store AWS region in client struct, for region specific operations such as
	// bucket storage in S3
	client.region = c.Region
	client.subProvider = subProvider

	log.Println("[INFO] Building AWS auth structure")
	creds, err := tfaws.GetCredentials(c)
	if err != nil {
		return nil, err
	}
	// Call Get to check for credential provider. If nothing found, we'll get an
	// error, and we can present it nicely to the user
	cp, err := creds.Get()
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == "NoCredentialProviders" {
			return nil, errors.New(`No valid credential sources found for AWS Provider.
  Please see https://terraform.io/docs/providers/aws/index.html for more information on
  providing credentials for the AWS Provider`)
		}

		return nil, fmt.Errorf("Error loading credentials for AWS Provider: %s", err)
	}

	log.Printf("[INFO] AWS Auth provider used: %q", cp.ProviderName)

	awsConfig := &aws.Config{
		Credentials:      creds,
		Region:           aws.String(c.Region),
		MaxRetries:       aws.Int(c.MaxRetries),
		HTTPClient:       cleanhttp.DefaultClient(),
		S3ForcePathStyle: aws.Bool(c.S3ForcePathStyle),
	}

	if c.Insecure {
		transport := awsConfig.HTTPClient.Transport.(*http.Transport)
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	// Set up base session
	sess, err := session.NewSession(awsConfig)
	if err != nil {
		return nil, errwrap.Wrapf("Error creating AWS session: {{err}}", err)
	}

	// Some services exist only in us-east-1, e.g. because they manage
	// resources that can span across multiple regions, or because
	// signature format v4 requires region to be us-east-1 for global
	// endpoints:
	// http://docs.aws.amazon.com/general/latest/gr/sigv4_changes.html
	usEast1Sess := sess.Copy(&aws.Config{Region: aws.String("us-east-1")})

	// Some services have user-configurable endpoints
	awsIamSess := sess.Copy(&aws.Config{Endpoint: aws.String(c.IamEndpoint)})

	// These two services need to be set up early so we can check on AccountID
	client.iamconn = iam.New(awsIamSess)
	client.stsconn = sts.New(sess)

	if !c.SkipCredsValidation {
		err = c.ValidateCredentials(client.stsconn)
		if err != nil {
			return nil, err
		}
	}

	if !c.SkipRequestingAccountId {
		partition, accountId, err := tfaws.GetAccountInfo(client.iamconn, client.stsconn, cp.ProviderName)
		if err == nil {
			client.partition = partition
			client.accountid = accountId
		}
	}

	authErr := c.ValidateAccountId(client.accountid)
	if authErr != nil {
		return nil, authErr
	}

	client.r53conn = route53.New(usEast1Sess)

	if subProvider != "" {
		providers[subProvider] = client
	}

	client.dxconn = directconnect.New(sess)

	return client, nil
}

func cleanPrefix(ID, prefix string) string {
	if strings.HasPrefix(ID, prefix) {
		ID = strings.TrimPrefix(ID, prefix)
	}

	return ID
}

func cleanChangeID(ID string) string {
	return cleanPrefix(ID, "/change/")
}

func resourceAwsGoRoute53Wait(r53 *route53.Route53, ref *route53.GetChangeInput) (result interface{}, state string, err error) {
	status, err := r53.GetChange(ref)

	if err != nil {
		return nil, "UNKNOWN", err
	}

	return true, *status.ChangeInfo.Status, nil
}
