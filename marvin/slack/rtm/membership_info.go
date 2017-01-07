package rtm

import (
	"github.com/riking/homeapi/marvin/slack"
)

type membershipMap map[slack.ChannelID]map[slack.UserID]bool

type membershipRequest struct {
	F func(membershipMap) interface{}
	C chan interface{}
}

func (c *Client) membershipWorker() {
	for req := range c.membershipCh {
		req.C <- req.F(c.channelMembers)
	}
}

func userInChannelList(user slack.UserID, channels ...slack.ChannelID) func(m membershipMap) interface{} {
	r := make(map[slack.ChannelID]bool)
	return func(m membershipMap) interface{} {
		for _, v := range channels {
			r[v] = m[v][user]
		}
		return r
	}
}

func userJoinChannel(user slack.UserID, channel slack.ChannelID, join bool) func(m membershipMap) interface{} {
	return func(m membershipMap) interface{} {
		m[channel][user] = join
		return nil
	}
}

func (c *Client) UserInChannels(user slack.UserID, channels ...slack.ChannelID) map[slack.ChannelID]bool {
	ch := make(chan interface{})
	c.membershipCh <- membershipRequest{C: ch,
		F: userInChannelList(user, channels...),
	}
	return (<-ch).(map[slack.ChannelID]bool)
}

func (c *Client) onUserJoinChannel(msg slack.RTMRawMessage) {
	ch := make(chan interface{}, 1)
	c.membershipCh <- membershipRequest{C: ch,
		F: userJoinChannel(msg.UserID(), msg.ChannelID(), true),
	}
}

func (c *Client) onUserLeaveChannel(msg slack.RTMRawMessage) {
	ch := make(chan interface{}, 1)
	c.membershipCh <- membershipRequest{C: ch,
		F: userJoinChannel(msg.UserID(), msg.ChannelID(), false),
	}
}