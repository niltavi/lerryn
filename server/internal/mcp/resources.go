package mcp

import (
	"context"
	"fmt"
	"path"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/skills"
)

// SkillURIPrefix starts the URI of every skill file served as an MCP
// resource: lerryn://skills/<skill>/<path>, for example
// lerryn://skills/lerryn-automation/SKILL.md (tech design 10.7).
const SkillURIPrefix = "lerryn://skills/"

// SkillURI returns the resource URI of a skill file.
func SkillURI(skill, file string) string { return SkillURIPrefix + skill + "/" + file }

// addSkillResources registers every file of the embedded skills as a
// resource, so that clients that read resources get the skills matching
// this server without a tool call.
func addSkillResources(srv *mcpsdk.Server) error {
	list, err := skills.List()
	if err != nil {
		return err
	}
	for _, s := range list {
		for _, file := range s.Files {
			data, err := skills.ReadFile(s.Name, file)
			if err != nil {
				return fmt.Errorf("skill file %s/%s: %w", s.Name, file, err)
			}
			r := &mcpsdk.Resource{
				URI:      SkillURI(s.Name, file),
				Name:     s.Name + "/" + file,
				MIMEType: mimeType(file),
				Size:     int64(len(data)),
			}
			if file == "SKILL.md" {
				r.Title, r.Description = s.Name, s.Description
			}
			srv.AddResource(r, skillFileHandler(s.Name, file))
		}
	}
	return nil
}

func skillFileHandler(skill, file string) mcpsdk.ResourceHandler {
	uri := SkillURI(skill, file)
	return func(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		data, err := skills.ReadFile(skill, file)
		if err != nil {
			return nil, mcpsdk.ResourceNotFoundError(req.Params.URI)
		}
		return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{
			{URI: uri, MIMEType: mimeType(file), Text: string(data)},
		}}, nil
	}
}

func mimeType(file string) string {
	switch strings.ToLower(path.Ext(file)) {
	case ".md":
		return "text/markdown"
	case ".py":
		return "text/x-python"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "text/plain"
	}
}
