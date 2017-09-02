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

	"github.com/dgraph-io/dgraph/conn"
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
	idMap map[uint64]protos.Membership
}

type Server struct {
	x.SafeMutex
	wal *raftwal.Wal

	NumReplicas int
	tablets     []protos.Tablet
	groupMap    map[uint32]*Group
	nextGroup   uint32
}

func (s *Server) createMembershipUpdate() protos.MembershipUpdate {
	return protos.MembershipUpdate{}
}

// Connect is used to connect the very first time with group zero.
func (s *Server) Connect(ctx context.Context,
	m *protos.Membership) (u *protos.MembershipUpdate, err error) {
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
	pl := conn.Get().Connect(m.Addr)
	defer conn.Get().Release(pl)

	if err := conn.TestConnection(pl); err != nil {
		// TODO: How do we delete this connection pool?
		return u, err
	}
	x.Printf("Connection successful to addr: %s", m.Addr)

	s.Lock()
	defer s.Unlock()
	if m.GroupId > 0 {
		group, has := s.groupMap[m.GroupId]
		if !has {
			// We don't have this group. Add the server to this group.
			group = &Group{idMap: make(map[uint64]protos.Membership)}
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
	group := &Group{idMap: make(map[uint64]protos.Membership)}
	group.idMap[m.Id] = *m
	s.groupMap[m.GroupId] = group
	return
}

func (s *Server) hasMember(ms *protos.Membership) bool {
	s.AssertRLock()
	group, has := s.groupMap[ms.GroupId]
	if !has {
		return false
	}
	_, has = group.idMap[ms.Id]
	return has
}

func (s *Server) ShouldServe(
	ctx context.Context, query *protos.MembershipQuery) (resp *protos.Payload, err error) {

	s.RLock()
	// First check if the caller is in the member list.
	if !s.hasMember(query.GetMember()) {
		s.RUnlock()
		resp.Data = []byte("wrong")
		return
	}

	tablet := query.GetTablet()
	var tgroup uint32 // group serving tablet.
	for _, t := range s.tablets {
		// Slightly slow, but happens infrequently enough that it's OK.
		if t.Predicate == tablet.Predicate {
			tgroup = t.GroupId
		}
	}
	s.RUnlock()

	if tgroup == 0 {
		// Set the tablet to be served by this server.
		return
	}

	// Someone is serving this tablet.
	if tgroup == query.Member.GroupId {
		// Tablet is being served by the caller.
		resp.Data = []byte("ok")
	} else {
		resp.Data = []byte("not")
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
