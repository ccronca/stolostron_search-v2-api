package graph

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.
// Code generated by github.com/99designs/gqlgen version v0.17.31

import (
	"context"

	"github.com/stolostron/search-v2-api/federatedsearch/graph/model"
	"github.com/stolostron/search-v2-api/pkg/resolver"
	"k8s.io/klog/v2"
)

// GlobalSearch is the resolver for the globalSearch field.
func (r *queryResolver) GlobalSearch(ctx context.Context, input []*model.SearchInput) ([]*model.SearchResult, error) {
	props := map[string]interface{}{
		"allProperties": []string{"cluster", "kind", "label", "name", "namespace", "status"},
	}
	items := model.SearchResult{Items: []map[string]interface{}{props}}

	srchResult := make([]*model.SearchResult, len(input))
	srchResult = append(srchResult, &items)
	return srchResult, nil
}

// SearchSchema is the resolver for the searchSchema field.
func (r *queryResolver) SearchSchema(ctx context.Context) (map[string]interface{}, error) {
	klog.V(3).Infoln("Received SearchSchema query")
	return resolver.SearchSchemaResolver(ctx)
}

// Query returns QueryResolver implementation.
func (r *Resolver) Query() QueryResolver { return &queryResolver{r} }

type queryResolver struct{ *Resolver }

// !!! WARNING !!!
// The code below was going to be deleted when updating resolvers. It has been copied here so you have
// one last chance to move it out of harms way if you want. There are two reasons this happens:
//   - When renaming or deleting a resolver the old code will be put in here. You can safely delete
//     it when you're done.
//   - You have helper methods in this file. Move them out to keep these resolver files clean.
type mutationResolver struct{ *Resolver }
