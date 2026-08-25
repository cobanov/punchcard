package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type listBody struct {
	Body ListDTO
}

func (d Deps) registerListRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "lists-list", Method: http.MethodGet, Path: "/v1/lists",
		Summary: "List accessible lists", Tags: []string{"lists"}, Errors: []int{401},
	}, func(ctx context.Context, _ *struct{}) (*struct {
		Body struct {
			Lists []ListDTO `json:"lists"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		lists, err := d.Domain.ListLists(ctx, p)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Lists []ListDTO `json:"lists"`
			}
		}{}
		out.Body.Lists = make([]ListDTO, 0, len(lists))
		for _, l := range lists {
			out.Body.Lists = append(out.Body.Lists, listDTO(l.List, l.Role))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "lists-create", Method: http.MethodPost, Path: "/v1/lists",
		Summary: "Create a list", Tags: []string{"lists"}, DefaultStatus: http.StatusCreated,
		Errors: []int{401, 403, 409, 422},
	}, func(ctx context.Context, in *struct {
		Body struct {
			ID   *string `json:"id,omitempty" format:"uuid" doc:"Optional client-generated UUIDv7 (offline sync). Same id again → 409 id_conflict."`
			Name string  `json:"name" minLength:"1" maxLength:"200"`
		}
	}) (*listBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		clientID, err := uuidPtr(in.Body.ID)
		if err != nil {
			return nil, err
		}
		list, err := d.Domain.CreateList(ctx, p, in.Body.Name, clientID)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &listBody{Body: listDTO(list, "owner")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "lists-get", Method: http.MethodGet, Path: "/v1/lists/{id}",
		Summary: "Get a list", Tags: []string{"lists"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*listBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		list, role, err := d.Domain.GetList(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &listBody{Body: listDTO(list, role)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "lists-update", Method: http.MethodPatch, Path: "/v1/lists/{id}",
		Summary: "Rename a list or set its colour", Tags: []string{"lists"}, Errors: []int{401, 403, 404, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			// Pointers, because this is a PATCH and an absent field has to mean
			// "leave it" rather than "set it to the zero value" — a rename that
			// arrived without a colour used to be indistinguishable from a
			// request to clear one.
			Name *string `json:"name,omitempty" minLength:"1" maxLength:"200"`
			// The empty string clears the colour; anything else must be one of
			// service.ListColors. Enumerated here as well so the value shows up
			// in docs/openapi.json, which is what an agent reads before writing.
			Color *string `json:"color,omitempty" enum:"red,amber,green,teal,blue,violet,pink,slate," doc:"A colour name, or \"\" to clear it"`
		}
	}) (*listBody, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		list, err := d.Domain.UpdateList(ctx, p, id, in.Body.Name, in.Body.Color)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &listBody{Body: listDTO(list, "owner")}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "lists-delete", Method: http.MethodDelete, Path: "/v1/lists/{id}",
		Summary: "Delete a list", Tags: []string{"lists"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404, 409},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.DeleteList(ctx, p, id); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	d.registerMemberRoutes(api)
	d.registerInviteRoutes(api)
}

func (d Deps) registerMemberRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "members-list", Method: http.MethodGet, Path: "/v1/lists/{id}/members",
		Summary: "List members", Tags: []string{"members"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Members []MemberDTO `json:"members"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		members, err := d.Domain.ListMembers(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Members []MemberDTO `json:"members"`
			}
		}{}
		out.Body.Members = make([]MemberDTO, 0, len(members))
		for _, m := range members {
			out.Body.Members = append(out.Body.Members, memberDTO(m))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "members-add", Method: http.MethodPost, Path: "/v1/lists/{id}/members",
		Summary: "Add a member by user id", Tags: []string{"members"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			UserID string `json:"user_id" format:"uuid"`
			Role   string `json:"role" enum:"owner,editor,viewer"`
		}
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		listID, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		userID, err := parseUUID(in.Body.UserID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.AddMember(ctx, p, listID, userID, in.Body.Role, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "members-update", Method: http.MethodPatch, Path: "/v1/lists/{id}/members/{user_id}",
		Summary: "Change a member's role", Tags: []string{"members"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		ID     string `path:"id" format:"uuid"`
		UserID string `path:"user_id" format:"uuid"`
		Body   struct {
			Role string `json:"role" enum:"owner,editor,viewer"`
		}
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		listID, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		userID, err := parseUUID(in.UserID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.UpdateMemberRole(ctx, p, listID, userID, in.Body.Role, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "members-remove", Method: http.MethodDelete, Path: "/v1/lists/{id}/members/{user_id}",
		Summary: "Remove a member", Tags: []string{"members"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404, 409},
	}, func(ctx context.Context, in *struct {
		ID     string `path:"id" format:"uuid"`
		UserID string `path:"user_id" format:"uuid"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		listID, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		userID, err := parseUUID(in.UserID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.RemoveMember(ctx, p, listID, userID, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})
}

func (d Deps) registerInviteRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "invites-list", Method: http.MethodGet, Path: "/v1/lists/{id}/invites",
		Summary: "List pending invites", Tags: []string{"invites"}, Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Invites []InviteDTO `json:"invites"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		invites, err := d.Domain.ListInvites(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Invites []InviteDTO `json:"invites"`
			}
		}{}
		out.Body.Invites = make([]InviteDTO, 0, len(invites))
		for _, inv := range invites {
			out.Body.Invites = append(out.Body.Invites, inviteDTO(inv))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "invites-create", Method: http.MethodPost, Path: "/v1/lists/{id}/invites",
		Summary: "Invite an email to a list", Tags: []string{"invites"}, DefaultStatus: http.StatusCreated,
		Errors: []int{401, 403, 404, 409, 422},
	}, func(ctx context.Context, in *struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			Email string `json:"email" format:"email"`
			Role  string `json:"role" enum:"owner,editor,viewer"`
		}
	}) (*struct {
		Body InviteDTO
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		inv, err := d.Domain.CreateInvite(ctx, p, id, in.Body.Email, in.Body.Role, clientIP(ctx))
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{ Body InviteDTO }{Body: inviteDTO(inv)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "invites-cancel", Method: http.MethodDelete, Path: "/v1/lists/{id}/invites/{invite_id}",
		Summary: "Cancel a pending invite", Tags: []string{"invites"}, DefaultStatus: http.StatusNoContent,
		Errors: []int{401, 403, 404},
	}, func(ctx context.Context, in *struct {
		ID       string `path:"id" format:"uuid"`
		InviteID string `path:"invite_id" format:"uuid"`
	}) (*struct{}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		listID, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		inviteID, err := parseUUID(in.InviteID)
		if err != nil {
			return nil, err
		}
		if err := d.Domain.CancelInvite(ctx, p, listID, inviteID, clientIP(ctx)); err != nil {
			return nil, d.problem(ctx, err)
		}
		return &struct{}{}, nil
	})
}
