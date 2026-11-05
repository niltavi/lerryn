package httpapi

import (
	"context"
	"fmt"
	"slices"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/skills"
)

// The skills embedded in the binary (tech design 11.6). They are public
// content that ships with the server, so no permission applies; their
// version is the server's.

// ListSkills implements listSkills.
func (h *handler) ListSkills(ctx context.Context, _ ListSkillsRequestObject) (ListSkillsResponseObject, error) {
	list, err := skills.List()
	if err != nil {
		return errorResponse[ListSkillsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]SkillInfo, len(list))
	for i, s := range list {
		items[i] = SkillInfo{Name: s.Name, Description: s.Description, Files: slices.Clone(s.Files)}
	}
	return ListSkills200JSONResponse{Version: h.server.Version, Items: items}, nil
}

// GetSkill implements getSkill.
func (h *handler) GetSkill(ctx context.Context, req GetSkillRequestObject) (GetSkillResponseObject, error) {
	if !slices.Contains(skills.Names, req.Name) {
		return errorResponse[GetSkilldefaultApplicationProblemPlusJSONResponse](ctx, service.ErrNotFound)
	}
	s, err := skills.Get(req.Name)
	if err != nil {
		return errorResponse[GetSkilldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	files := make([]SkillFile, len(s.Files))
	for i, p := range s.Files {
		data, err := skills.ReadFile(s.Name, p)
		if err != nil {
			return errorResponse[GetSkilldefaultApplicationProblemPlusJSONResponse](ctx, fmt.Errorf("read skill file %s/%s: %w", s.Name, p, err))
		}
		files[i] = SkillFile{Path: p, Content: string(data)}
	}
	return GetSkill200JSONResponse{Name: s.Name, Description: s.Description, Version: h.server.Version, Files: files}, nil
}

// skillSummaries is the skill list of discover.
func skillSummaries() ([]SkillSummary, error) {
	list, err := skills.List()
	if err != nil {
		return nil, err
	}
	out := make([]SkillSummary, len(list))
	for i, s := range list {
		out[i] = SkillSummary{Name: s.Name, Description: s.Description}
	}
	return out, nil
}
