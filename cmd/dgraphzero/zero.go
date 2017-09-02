/*
 * Copyright (C) 2017 Dgraph Labs, Inc. and Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"errors"

	"golang.org/x/net/context"

	"github.com/dgraph-io/dgraph/protos"
	"github.com/dgraph-io/dgraph/raftwal"
	"github.com/dgraph-io/dgraph/x"
)

var (
	emptyMembershipUpdate protos.MembershipUpdate
	ErrInvalidId          = errors.New("Invalid server id")
	ErrInvalidAddress     = errors.New("Invalid address")
)

type Group struct {
	idMap map[uint64]Membership
}

type Server struct {
	x.SafeMutex
	wal *raftwal.Wal

	NumReplicas int
	tablets     []Tablet
	groupMap    map[uint32]*Group
	nextGroup   uint32
}

func (s *Server) createMembershipUpdate() protos.MembershipUpdate {
}

// Connect is used to connect the very first time with group zero.
func (s *Server) Connect(ctx context.Context,
	m *protos.Membership) (u protos.MembershipUpdate, err error) {
	if ctx.Err() != nil {
		return &emptyMembershipUpdate, ctx.Err()
	}
	if m.Id == 0 {
		return u, ErrInvalidId
	}
	if len(m.Addr) == 0 {
		return u, ErrInvalidAddress
	}
	// Create a connection and check validity of the address by doing an Echo.

	s.Lock()
	defer s.Unlock()
	if m.GroupId > 0 {
		group, has := groupMap[m.GroupId]
		if !has {
			// We don't have this group. Add the server to this group.
			group = &Group{idMap: make(map[uint64]Membership)}
			group.idMap[m.Id] = *m
			s.groupMap[m.GroupId] = group
			// TODO: Propose these updates to Raft before applying.
			return
		}

		if _, has := group.idMap[m.Id]; has {
			group.idMap[m.Id] = *m // Update in case some fields have changed, like address.
			return
		}
		// We don't have this server in the list.
		if len(group.idMap) < s.NumReplicas {
			// We need more servers here, so let's add it.
			group.idMap[m.Id] = *m
			// TODO: Update the cluster about this.
			return
		}
		// Already have plenty of servers serving this group.
	}
	// Let's assign this server to a new group.
	for gid, group := range s.groupMap {
		if len(group.idMap) < s.NumReplicas {
			m.GroupId = gid
			group.idMap[m.Id] = *m
			return
		}
	}
	// We either don't have any groups, or don't have any groups which need another member.
	m.GroupId = s.nextGroup
	s.nextGroup++
	group = &Group{idMap: make(map[uint64]Membership)}
	group.idMap[m.Id] = *m
	s.groupMap[m.GroupId] = group
	return
}

func (s *Server) hasMember(ms *protos.Membership) bool {
	s.AssertRLock()
	for _, m := range s.members {
		if m.Id == ms.Id && m.GroupId == ms.GroupId {
			return true
		}
	}
	return false
}

func (s *Server) ShouldServe(
	ctx context.Context, query *protos.MembershipQuery) (resp *protos.Payload, err error) {

	s.RLock()
	// First check if the caller is in the member list.
	var found bool
	for _, m := range members {
		qm := query.GetMember()
		if m.Id == qm.Id {
			// Id matches, do the rest match as well?
			if m.GroupId != qm.GroupId {
				// Error with the server.
				resp.Data = []byte("wrong")
				s.RUnlock()
				return
			}
			// Both id and group id match.
			found = true
		}
	}
	if !found {
		s.RUnlock()
		resp.Data = []byte("wrong")
		return
	}

	var group uint32
	for _, t := range s.tablets {
		// Slightly slow, but happens infrequently enough that it's OK.
		if t.Predicate == tablet.Predicate {
			group = t.GroupId
		}
	}
	s.RUnlock()

	if group > 0 {
		// Someone is serving this tablet.
		if group == tablet.GetGroupId() {
			// Tablet is being served by the caller.
			resp.Data = []byte("ok")
		} else {
			resp.Data = []byte("not")
		}
		return
	}

	// No one is serving this tablet.
	if tablet.GetGroupId() == 0 {
		// The caller has no group assigned.
		resp.Data = []byte("ok")
	}
	return
}

func (s *Server) Update(
	ctx context.Context, membership *protos.Membership) (resp *protos.MembershipUpdate, err error) {
	if ctx.Err() != nil {
		return &emptyMembershipUpdate, ctx.Err()
	}
	return
}
