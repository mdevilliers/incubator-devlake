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

package tasks

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/apache/incubator-devlake/core/dal"
	"github.com/apache/incubator-devlake/core/errors"
	"github.com/apache/incubator-devlake/core/models/domainlayer/ticket"
	"github.com/apache/incubator-devlake/core/plugin"
	"github.com/apache/incubator-devlake/helpers/pluginhelper/api"
	helper "github.com/apache/incubator-devlake/helpers/pluginhelper/api"
	"github.com/apache/incubator-devlake/plugins/gitlab/models"
)

func init() {
	RegisterSubtaskMeta(&EnrichMergeRequestsMeta)
}

var EnrichMergeRequestsMeta = plugin.SubTaskMeta{
	Name:             "Enrich Merge Requests",
	EntryPoint:       EnrichMergeRequests,
	EnabledByDefault: true,
	Description:      "Enrich merge requests data from GitlabCommit, GitlabMrNote and GitlabMergeRequest",
	DomainTypes:      []string{plugin.DOMAIN_TYPE_CODE_REVIEW},
	Dependencies:     []*plugin.SubTaskMeta{&ExtractApiJobsMeta},
}

var EnrichMergeRequestIssuesMeta = plugin.SubTaskMeta{
	Name:             "Enrich Merge Request with Issues",
	EntryPoint:       EnrichMergeRequestIssues,
	EnabledByDefault: true,
	Description:      "Create tool layer table gitlab_pull_request_issues from gitlab_merge_requests",
	DomainTypes:      []string{plugin.DOMAIN_TYPE_CROSS},
	DependencyTables: []string{
		models.GitlabMergeRequest{}.TableName(), // cursor
		RAW_MERGE_REQUEST_TABLE},
	ProductTables: []string{models.GitlabMrIssue{}.TableName()},
}

func EnrichMergeRequests(taskCtx plugin.SubTaskContext) errors.Error {
	rawDataSubTaskArgs, data := CreateRawDataSubTaskArgs(taskCtx, RAW_MERGE_REQUEST_TABLE)

	db := taskCtx.GetDal()
	clauses := []dal.Clause{
		dal.From(&models.GitlabMergeRequest{}),
		dal.Where("project_id=? and connection_id = ?", data.Options.ProjectId, data.Options.ConnectionId),
	}

	cursor, err := db.Cursor(clauses...)
	if err != nil {
		return err
	} // get mrs from theDB
	defer cursor.Close()

	converter, err := helper.NewDataConverter(helper.DataConverterArgs{
		RawDataSubTaskArgs: *rawDataSubTaskArgs,
		InputRowType:       reflect.TypeOf(models.GitlabMergeRequest{}),
		Input:              cursor,

		Convert: func(inputRow interface{}) ([]interface{}, errors.Error) {
			gitlabMr := inputRow.(*models.GitlabMergeRequest)
			// enrich first_comment_time field
			notes := make([]models.GitlabMrNote, 0)
			// `system` = 0 is needed since we only care about human comments
			noteClauses := []dal.Clause{
				dal.From(&models.GitlabMrNote{}),
				dal.Where("merge_request_id = ? AND is_system = ? AND connection_id = ? ",
					gitlabMr.GitlabId, false, data.Options.ConnectionId),
				dal.Orderby("gitlab_created_at asc"),
			}
			err = db.All(&notes, noteClauses...)
			if err != nil {
				return nil, err
			}

			commits := make([]models.GitlabCommit, 0)
			commitClauses := []dal.Clause{
				dal.From(&models.GitlabCommit{}),
				dal.Join(`join _tool_gitlab_mr_commits gmrc
					on gmrc.commit_sha = _tool_gitlab_commits.sha`),
				dal.Where("merge_request_id = ? AND gmrc.connection_id = ?",
					gitlabMr.GitlabId, data.Options.ConnectionId),
				dal.Orderby("authored_date asc"),
			}
			err = db.All(&commits, commitClauses...)
			if err != nil {
				return nil, err
			}

			// calculate reviewRounds from commits and notes
			reviewRounds := getReviewRounds(commits, notes)
			gitlabMr.ReviewRounds = reviewRounds

			if len(notes) > 0 {
				earliestNote, err := findEarliestNote(notes)
				if err != nil {
					return nil, err
				}
				if earliestNote != nil {
					gitlabMr.FirstCommentTime = &earliestNote.GitlabCreatedAt
				}
			}
			return []interface{}{
				gitlabMr,
			}, nil
		},
	})
	if err != nil {
		return err
	}

	return converter.Execute()
}

func findEarliestNote(notes []models.GitlabMrNote) (*models.GitlabMrNote, errors.Error) {
	var earliestNote *models.GitlabMrNote
	earliestTime := time.Now()
	for i := range notes {
		if !notes[i].Resolvable {
			continue
		}
		noteTime := notes[i].GitlabCreatedAt
		if noteTime.Before(earliestTime) {
			earliestTime = noteTime
			earliestNote = &notes[i]
		}
	}
	return earliestNote, nil
}

func getReviewRounds(commits []models.GitlabCommit, notes []models.GitlabMrNote) int {
	i := 0
	j := 0
	reviewRounds := 0
	if len(commits) == 0 && len(notes) == 0 {
		return 1
	}
	// state is used to keep track of previous activity
	// 0: init, 1: commit, 2: comment
	// whenever state is switched to comment, we increment reviewRounds by 1
	state := 0 // 0, 1, 2
	for i < len(commits) && j < len(notes) {
		if commits[i].AuthoredDate.Before(notes[j].GitlabCreatedAt) {
			i++
			state = 1
		} else {
			j++
			if state != 2 {
				reviewRounds++
			}
			state = 2
		}
	}
	// There's another implicit round of review in 2 scenarios
	// One: the last state is commit (state == 1)
	// Two: the last state is comment but there're still commits left
	if state == 1 || i < len(commits) {
		reviewRounds++
	}
	return reviewRounds
}

func EnrichMergeRequestIssues(taskCtx plugin.SubTaskContext) (err errors.Error) {
	db := taskCtx.GetDal()
	rawDataSubTaskArgs, data := CreateRawDataSubTaskArgs(taskCtx, RAW_MERGE_REQUEST_TABLE)

	var mrIssueRegex *regexp.Regexp
	mrIssuePattern := data.Options.ScopeConfig.IssueRegex

	//the pattern before the issue number, sometimes, the issue number is #1098, sometimes it is https://xxx/#1098
	mrIssuePattern = strings.Replace(mrIssuePattern, "%s", data.Options.FullName, 1)
	if len(mrIssuePattern) > 0 {
		mrIssueRegex, err = errors.Convert01(regexp.Compile(mrIssuePattern))
		if err != nil {
			return errors.Default.Wrap(err, "regexp Compile mrIssuePattern failed")
		}
	}
	cursor, err := db.Cursor(dal.From(&models.GitlabMergeRequest{}),
		dal.Where("project_id = ? AND connection_id = ?", data.Options.ProjectId, data.Options.ConnectionId))
	if err != nil {
		return err
	}
	defer cursor.Close()
	// iterate all rows
	converter, err := api.NewDataConverter(api.DataConverterArgs{
		InputRowType:       reflect.TypeOf(models.GitlabMergeRequest{}),
		Input:              cursor,
		RawDataSubTaskArgs: *rawDataSubTaskArgs,
		Convert: func(inputRow interface{}) ([]interface{}, errors.Error) {
			gitlabMrRequest := inputRow.(*models.GitlabMergeRequest)

			//find the issue in the body
			issueNumberStr := ""

			if mrIssueRegex != nil {
				issueNumberStr = mrIssueRegex.FindString(gitlabMrRequest.Description)
			}
			//find the issue in the title
			if issueNumberStr == "" {
				issueNumberStr = mrIssueRegex.FindString(gitlabMrRequest.Title)
			}

			if issueNumberStr == "" {
				return nil, nil
			}

			issueNumberStr = strings.ReplaceAll(issueNumberStr, "#", "")
			issueNumberStr = strings.TrimSpace(issueNumberStr)

			issue := &ticket.Issue{}

			//change the issueNumberStr to int, if cannot be changed, just continue
			issueNumber, numFormatErr := strconv.Atoi(issueNumberStr)
			if numFormatErr != nil {
				return nil, nil
			}
			err = db.All(
				issue,
				dal.Where("issue_key = ?",
					issueNumber),
				dal.Limit(1),
			)
			if err != nil {
				return nil, err
			}
			//fmt.Println("found one:", issueNumberStr, issue)
			// TODO : figure out why the _raw_xxx is not being saved.
			pullRequestIssue := &models.GitlabMrIssue{
				ConnectionId:  data.Options.ConnectionId,
				PullRequestId: gitlabMrRequest.GitlabId,
				IssueId:       issue.Id,
			}

			return []interface{}{pullRequestIssue}, nil
		},
	})
	if err != nil {
		return err
	}

	return converter.Execute()
}
