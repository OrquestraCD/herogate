package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go/service/codebuild"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/sirupsen/logrus"
	"github.com/wata727/herogate/api/options"
	"github.com/wata727/herogate/log"
)

// DescribeLogs returns the Herogate application logs.
// In this function, it calls CodeBuild API, CloudWatchLogs API, and ECS Service API internally
// and sorts logs by timestamps.
func (c *Client) DescribeLogs(appName string, options *options.DescribeLogs) ([]*log.Log, error) {
	if options == nil {
		return []*log.Log{}, nil
	}

	var logs []*log.Log = []*log.Log{}
	switch options.Source {
	case "":
		fallthrough
	case log.HerogateSource:
		switch options.Process {
		case "":
			builderLogs, err := c.describeBuilderLogs(appName)
			if err != nil {
				return []*log.Log{}, err
			}
			deployerLogs, err := c.describeDeployerLogs(appName)
			if err != nil {
				return []*log.Log{}, err
			}
			logs = append(builderLogs, deployerLogs...)
		case log.BuilderProcess:
			builderLogs, err := c.describeBuilderLogs(appName)
			if err != nil {
				return []*log.Log{}, err
			}
			logs = builderLogs
		case log.DeployerProcess:
			deployerLogs, err := c.describeDeployerLogs(appName)
			if err != nil {
				return []*log.Log{}, err
			}
			logs = deployerLogs
		}
	}

	// TODO: Keep original order in the same time
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Timestamp.Before(logs[j].Timestamp)
	})

	return logs, nil
}

func (c *Client) describeBuilderLogs(appName string) ([]*log.Log, error) {
	listBuildsForProjectResponse, err := c.codeBuild.ListBuildsForProject(&codebuild.ListBuildsForProjectInput{
		ProjectName: aws.String(appName),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == codebuild.ErrCodeResourceNotFoundException {
			return []*log.Log{}, err
		}

		logrus.WithFields(logrus.Fields{
			"ProjectName": appName,
		}).Fatal("Failed to get the project: " + err.Error())
	}
	if len(listBuildsForProjectResponse.Ids) == 0 {
		return []*log.Log{}, nil
	}

	buildID := listBuildsForProjectResponse.Ids[0]
	batchGetBuildsResponse, err := c.codeBuild.BatchGetBuilds(&codebuild.BatchGetBuildsInput{
		Ids: []*string{buildID},
	})
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"Build ID": buildID,
		}).Fatal("Failed to get the build: " + err.Error())
	}
	if len(batchGetBuildsResponse.Builds) == 0 {
		return []*log.Log{}, nil
	}

	group := batchGetBuildsResponse.Builds[0].Logs.GroupName
	stream := batchGetBuildsResponse.Builds[0].Logs.StreamName
	getLogEventsResponse, err := c.cloudWatchLogs.GetLogEvents(&cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  group,
		LogStreamName: stream,
	})
	if err != nil {
		logrus.WithFields(logrus.Fields{
			"LogGroupName":  group,
			"LogStreamName": stream,
		}).Fatal("Failed to get the build logs: " + err.Error())
	}

	var logs []*log.Log = []*log.Log{}
	for _, event := range getLogEventsResponse.Events {
		logs = append(logs, &log.Log{
			ID:        fmt.Sprintf("%s-%d-%s", aws.StringValue(buildID), aws.Int64Value(event.Timestamp), aws.StringValue(event.Message)),
			Timestamp: aws.MillisecondsTimeValue(event.Timestamp).UTC(),
			Source:    log.HerogateSource,
			Process:   log.BuilderProcess,
			Message:   strings.TrimRight(aws.StringValue(event.Message), "\n"),
		})
	}

	return logs, nil
}

func (c *Client) describeDeployerLogs(appName string) ([]*log.Log, error) {
	resp, err := c.ecs.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  aws.String(appName),
		Services: []*string{aws.String(appName)},
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == ecs.ErrCodeClusterNotFoundException {
			return []*log.Log{}, err
		}

		logrus.WithFields(logrus.Fields{
			"appName": appName,
		}).Fatal("Failed to get the ECS service: " + err.Error())
	}
	if len(resp.Services) == 0 {
		return []*log.Log{}, nil
	}

	var logs []*log.Log = []*log.Log{}
	for _, event := range resp.Services[0].Events {
		logs = append(logs, &log.Log{
			ID:        aws.StringValue(event.Id),
			Timestamp: aws.TimeValue(event.CreatedAt),
			Source:    log.HerogateSource,
			Process:   log.DeployerProcess,
			Message:   aws.StringValue(event.Message),
		})
	}

	return logs, nil
}
