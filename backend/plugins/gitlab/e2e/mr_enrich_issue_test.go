/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"testing"

	"github.com/apache/incubator-devlake/core/models/domainlayer/ticket"
	"github.com/apache/incubator-devlake/helpers/e2ehelper"
	"github.com/apache/incubator-devlake/plugins/gitlab/impl"
	"github.com/apache/incubator-devlake/plugins/gitlab/models"
	"github.com/apache/incubator-devlake/plugins/gitlab/tasks"
)

func TestMrEnrichIssueDataFlow(t *testing.T) {
	var plugin impl.Gitlab
	dataflowTester := e2ehelper.NewDataFlowTester(t, "gitlab", plugin)

	taskData := &tasks.GitlabTaskData{
		Options: &tasks.GitlabOptions{
			ConnectionId: 1,
			ProjectId:    12345678,
			ScopeConfig: &models.GitlabScopeConfig{
				// PrBodyClosePattern:   "(?mi)(fix|close|resolve|fixes|closes|resolves|fixed|closed|resolved)[\\s]*.*(((and )?(#|https:\\/\\/github.com\\/%s\\/issues\\/)\\d+[ ]*)+)",
				// https://docs.gitlab.com/ee/user/project/issues/crosslinking_issues.html
				// For gitlab issues #xxx, GL-xxxx, projectname#xxx or https://gitlab.com/<username>/<projectname>/-/issues/<xxx>
				// https://regex101.com/r/RteyFk/1
				// TODO - look for JIRA issues
				IssueRegex: "(#|GL-|https://gitlab\\.com/([\\w]*)/([\\w]*)/-/issues/)([0-9]*)",
			},
		},
	}

	// import issues csv
	dataflowTester.ImportCsvIntoTabler("./snapshot_tables/issues.csv", &ticket.Issue{})

	// import processed merge requests csv
	dataflowTester.ImportCsvIntoTabler("./snapshot_tables/_tool_gitlab_merge_requests.csv", models.GitlabMergeRequest{})

	// verify extraction
	dataflowTester.FlushTabler(&models.GitlabMrIssue{})
	dataflowTester.Subtask(tasks.EnrichMergeRequestIssuesMeta, taskData)
	dataflowTester.VerifyTable(
		models.GitlabMrIssue{},
		"./snapshot_tables/_tool_gitlab_merge_request_issues.csv",
		[]string{
			"connection_id",
			"pull_request_id",
			"issue_id",
			"_raw_data_params",
			"_raw_data_table",
			"_raw_data_id",
			"_raw_data_remark",
		},
	)
	/*
	   // verify extraction
	   dataflowTester.FlushTabler(&crossdomain.PullRequestIssue{})
	   dataflowTester.Subtask(tasks.ConvertIssuesMeta, taskData)
	   dataflowTester.VerifyTable(

	   	crossdomain.PullRequestIssue{},
	   	"./snapshot_tables/pull_request_issues.csv",
	   	[]string{
	   		"pull_request_id",
	   		"issue_id",
	   		"pull_request_key",
	   		"issue_key",
	   		"_raw_data_params",
	   		"_raw_data_table",
	   		"_raw_data_id",
	   		"_raw_data_remark",
	   	},

	   )
	*/
}
