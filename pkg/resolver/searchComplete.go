package resolver

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/driftprogramming/pgxpoolmock"
	"github.com/stolostron/search-v2-api/graph/model"
	"github.com/stolostron/search-v2-api/pkg/config"
	db "github.com/stolostron/search-v2-api/pkg/database"
	klog "k8s.io/klog/v2"
)

type SearchCompleteResult struct {
	input    *model.SearchInput
	pool     pgxpoolmock.PgxPool
	property string
	limit    *int
	query    string
	params   []interface{}
}

func (s *SearchCompleteResult) autoComplete(ctx context.Context) ([]*string, error) {
	s.searchCompleteQuery(ctx)
	res, autoCompleteErr := s.searchCompleteResults(ctx)
	if autoCompleteErr != nil {
		klog.Error("Error resolving properties in autoComplete. ", autoCompleteErr)
	}
	return res, autoCompleteErr
}

func SearchComplete(ctx context.Context, property string, srchInput *model.SearchInput, limit *int) ([]*string, error) {

	searchCompleteResult := &SearchCompleteResult{
		input:    srchInput,
		pool:     db.GetConnection(),
		property: property,
		limit:    limit,
	}
	return searchCompleteResult.autoComplete(ctx)

}

// Sample query: SELECT DISTINCT name FROM
// (SELECT "data"->>'name' as name FROM "search"."resources" WHERE ("data"->>'name' IS NOT NULL)
// LIMIT 100000) as searchComplete
// ORDER BY name ASC
// LIMIT 1000
func (s *SearchCompleteResult) searchCompleteQuery(ctx context.Context) {
	var limit int
	var whereDs []exp.Expression
	var selectDs *goqu.SelectDataset

	schemaTable := goqu.S("search").Table("resources")
	ds := goqu.From(schemaTable)
	if s.property != "" {
		//WHERE CLAUSE
		if s.input != nil && len(s.input.Filters) > 0 {
			whereDs = WhereClauseFilter(s.input)
		}

		//SELECT CLAUSE
		if s.property == "cluster" {
			selectDs = ds.SelectDistinct(goqu.C(s.property).As("prop"))
			//Adding notNull clause to filter out NULL values and ORDER by sort results
			whereDs = append(whereDs, goqu.C(s.property).IsNotNull(),
				goqu.C(s.property).Neq("")) // remove empty strings from results
		} else {
			selectDs = ds.Select(goqu.L(`"data"->>?`, s.property).As("prop"))
			//Adding notNull clause to filter out NULL values and ORDER by sort results
			whereDs = append(whereDs, goqu.L(`"data"->>?`, s.property).IsNotNull())
		}
		//Adding an arbitrarily high number 100000 as limit here in the inner query
		// Adding a LIMIT helps to speed up the query
		// Adding a high number so as to get almost all the distinct properties from the database
		selectDs = selectDs.Where(whereDs...).Limit(uint(config.Cfg.QueryLimit) * 100).As("searchComplete")
		//LIMIT CLAUSE
		if s.limit != nil && *s.limit > 0 {
			limit = *s.limit
		} else if s.limit != nil && *s.limit == -1 {
			klog.Warning("No limit set. Fetching all results.")
		} else {
			limit = config.Cfg.QueryLimit
		}
		//Get the query
		sql, params, err := ds.SelectDistinct("prop").From(selectDs).Order(goqu.L("prop").Asc()).
			Limit(uint(limit)).ToSQL()
		if err != nil {
			klog.Errorf("Error building SearchComplete query: %s", err.Error())
		}
		s.query = sql
		s.params = params
		klog.V(3).Info("SearchComplete Query: ", s.query)
	} else {
		s.query = ""
		s.params = nil
	}

}

func (s *SearchCompleteResult) searchCompleteResults(ctx context.Context) ([]*string, error) {
	klog.V(2).Info("Resolving searchCompleteResults()")
	rows, err := s.pool.Query(ctx, s.query, s.params...)
	srchCompleteOut := make([]*string, 0)

	if err != nil {
		klog.Error("Error fetching search complete results from db ", err)
		return srchCompleteOut, err
	}
	defer rows.Close()
	if rows != nil {
		for rows.Next() {
			prop := ""
			scanErr := rows.Scan(&prop)
			if scanErr != nil {
				klog.Error("Error reading searchCompleteResults", scanErr)
			}
			srchCompleteOut = append(srchCompleteOut, &prop)
		}
	}
	isNumber := isNumber(srchCompleteOut)
	if isNumber { //check if valid number
		isNumber := "isNumber"
		srchCompleteOutNum := []*string{&isNumber} //isNumber should be the first argument if the property is a number
		// Sort the values in srchCompleteOut
		sort.Slice(srchCompleteOut, func(i, j int) bool {
			numA, _ := strconv.Atoi(*srchCompleteOut[i])
			numB, _ := strconv.Atoi(*srchCompleteOut[j])
			return numA < numB
		})
		if len(srchCompleteOut) > 1 {
			// Pass only the min and max values of the numbers to show the range in the UI
			srchCompleteOut = append(srchCompleteOutNum, srchCompleteOut[0], srchCompleteOut[len(srchCompleteOut)-1])
		} else {
			srchCompleteOut = append(srchCompleteOutNum, srchCompleteOut...)
		}

	}
	if !isNumber && isDate(srchCompleteOut) { //check if valid date
		isDate := "isDate"
		srchCompleteOutNum := []*string{&isDate}
		srchCompleteOut = srchCompleteOutNum
	}

	return srchCompleteOut, nil
}

// check if a given string is of type date
func isDate(vals []*string) bool {
	for _, val := range vals {
		// parse string date to golang time format: YYYY-MM-DDTHH:mm:ssZ i.e. "2022-01-01T17:17:09Z"
		// const time.RFC3339 is YYYY-MM-DDTHH:mm:ssZ format ex:"2006-01-02T15:04:05Z07:00"

		if _, err := time.Parse(time.RFC3339, *val); err != nil {
			return false
		}
	}
	return true
}

// check if a given string is of type number (int)
func isNumber(vals []*string) bool {

	for _, val := range vals {
		if _, err := strconv.Atoi(*val); err != nil {
			return false
		}
	}
	return true
}
