package main

// This file holds the "outbox" commands: list and cancel. They let the user stop
// a send that waits to go out, from a terminal. They work at any level of the
// account, so the user can first set the level to Off and then cancel what
// waits. The sealed message is never opened, so the list has no subject and no
// recipients.

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/EmadMokhtar/email-mcp-go/internal/outbox"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// outboxCommands are the subcommands of "outbox".
var outboxCommands = []command{
	{name: "list", summary: "list the sends that wait to go out", run: runOutboxList},
	{name: "cancel", summary: "stop a send that waits to go out: cancel ID", run: runOutboxCancel},
}

func runOutbox(ctx context.Context, c *cli, args []string) int {
	return runGroup(ctx, c, "outbox", outboxCommands, args)
}

func runOutboxList(ctx context.Context, c *cli, args []string) int {
	const cmd = "outbox list"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return c.unexpected(cmd)
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	list, err := a.Service.QueuedSends(userContext(ctx))
	if err != nil {
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	if len(list) == 0 {
		c.printf("No message waits to go out.\n")
		return exitOK
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tACCOUNT\tGOES OUT\tSTATE")
	for _, q := range list {
		state := "waits to go out"
		if q.Attempts > 0 {
			state = "will try again"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", q.ID, q.Address, formatTime(q.GoesOut), state)
	}
	_ = tw.Flush()
	if len(list) >= service.MaxQueuedSends {
		c.errorf("This list shows at most %d sends. Cancel these, and list them again to see the others.\n", service.MaxQueuedSends)
	}
	return exitOK
}

func runOutboxCancel(ctx context.Context, c *cli, args []string) int {
	const cmd = "outbox cancel"
	fs := c.newFlags(cmd)
	dataDir := fs.String("data-dir", "", dataDirUsage)
	rest, code, ok := c.parseFlags(cmd, fs, args)
	if !ok {
		return code
	}
	if len(rest) != 1 {
		return c.usage(cmd, "give one send id (\"email-mcp outbox list\" shows them)")
	}
	a := c.openApp(ctx, cmd, *dataDir)
	if a == nil {
		return exitError
	}
	defer c.closeApp(ctx, cmd, a)
	err := a.Service.CancelQueuedSend(userContext(ctx), rest[0])
	switch {
	case errors.Is(err, service.ErrSendNotFound):
		c.errorf("%s: No queued send has this id. \"email-mcp outbox list\" shows the ids.\n", cmd)
		return exitError
	case errors.Is(err, outbox.ErrTooLate):
		c.errorf("%s: It is too late to cancel this send: it is being sent, or it has ended.\n", cmd)
		return exitError
	case err != nil:
		c.errorf("%s: %v\n", cmd, err)
		return exitError
	}
	c.printf("Cancelled the send %s. It will not go out.\n", rest[0])
	return exitOK
}
