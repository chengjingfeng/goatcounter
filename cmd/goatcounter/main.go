// Copyright © 2019 Martin Tournoij – This file is part of GoatCounter and
// published under the terms of a slightly modified EUPL v1.2 license, which can
// be found in the LICENSE file or at https://license.goatcounter.com

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	_ "time/tzdata"

	"zgo.at/errors"
	"zgo.at/goatcounter"
	"zgo.at/goatcounter/db/migrate/gomig"
	"zgo.at/zdb"
	"zgo.at/zli"
	"zgo.at/zlog"
	"zgo.at/zstd/zfs"
	"zgo.at/zstd/zruntime"
	"zgo.at/zstd/zstring"
)

func init() {
	errors.Package = "zgo.at/goatcounter"
}

type command func(f zli.Flags, ready chan<- struct{}, stop chan struct{}) error

func main() {
	var (
		f     = zli.NewFlags(os.Args)
		ready = make(chan struct{}, 1)
		stop  = make(chan struct{}, 1)
	)
	cmdMain(f, ready, stop)
}

var mainDone sync.WaitGroup

func cmdMain(f zli.Flags, ready chan<- struct{}, stop chan struct{}) {
	mainDone.Add(1)
	defer mainDone.Done()

	cmd := f.ShiftCommand()
	if zstring.ContainsAny(f.Args, "-h", "-help", "--help") {
		f.Args = append([]string{cmd}, f.Args...)
		cmd = "help"
	}

	var run command
	switch cmd {
	default:
		zli.Errorf(usage[""])
		zli.Errorf("unknown command: %q", cmd)
		zli.Exit(1)
		return
	case "", "help", zli.CommandNoneGiven:
		run = cmdHelp
	case "version":
		fmt.Fprintln(zli.Stdout, getVersion())
		zli.Exit(0)
		return

	case "db", "database":
		run = cmdDB
	case "reindex":
		run = cmdReindex
	case "serve":
		run = cmdServe
	case "saas":
		run = cmdSaas
	case "monitor":
		run = cmdMonitor
	case "import":
		run = cmdImport
	case "buffer":
		run = cmdBuffer

	// Old commands; print some guidance instead of just "command doesn't
	// exist".
	// TODO: remove in 2.1 or 2.2
	case "migrate":
		fmt.Fprintf(zli.Stderr,
			"The migrate command is moved to \"goatcounter db migrate\"\n\n\t$ goatcounter db migrate %s\n",
			strings.Join(os.Args[2:], " "))
		zli.Exit(5)
		return
	case "create":
		flags := os.Args[2:]
		for i, ff := range flags {
			if ff == "-domain" {
				flags[i] = "-vhost"
			}
			if strings.HasPrefix(ff, "-domain=") {
				flags[i] = "-vhost=" + ff[8:]
			}
		}
		fmt.Fprintf(zli.Stderr,
			"The create command is moved to \"goatcounter db create site\"\n\n\t$ goatcounter db create site %s\n",
			strings.Join(flags, " "))
		zli.Exit(5)
		return
	}

	err := run(f, ready, stop)
	if err != nil {
		if !zstring.Contains(zlog.Config.Debug, "cli-trace") {
			for {
				var s *errors.StackErr
				if !errors.As(err, &s) {
					break
				}
				err = s.Unwrap()
			}
		}

		c := 1
		var stErr interface {
			Code() int
			Error() string
		}
		if errors.As(err, &stErr) {
			c = stErr.Code()
			if c > 255 { // HTTP error.
				c = 1
			}
		}

		if c == 0 {
			if err.Error() != "" {
				fmt.Fprintln(zli.Stdout, err.Error())
			}
			zli.Exit(0)
		}
		zli.Errorf(err)
		zli.Exit(c)
		return
	}
	zli.Exit(0)
}

func connectDB(connect string, migrate []string, create, dev bool) (zdb.DB, context.Context, error) {
	fsys, err := zfs.EmbedOrDir(goatcounter.DB, "db", dev)
	if err != nil {
		return nil, nil, err
	}

	db, err := zdb.Connect(zdb.ConnectOptions{
		Connect:      connect,
		Files:        fsys,
		Migrate:      migrate,
		GoMigrations: gomig.Migrations,
		Create:       create,
		SQLiteHook:   goatcounter.SQLiteHook,
		MigrateLog:   func(name string) { zlog.Printf("running migration %q", name) },
	})
	var pErr *zdb.PendingMigrationsError
	if errors.As(err, &pErr) {
		zlog.Errorf("%s; continuing but things may be broken", err)
		err = nil
	}
	var cErr *zdb.NotExistError
	if errors.As(err, &cErr) {
		// TODO: maybe ask for confirmation here?
		err = fmt.Errorf("%s database at %q doesn't exist.\n"+
			"Add the -createdb flag to create this database if you're sure this is the right location",
			cErr.Driver, cErr.DB)
	}
	if err != nil {
		return nil, nil, err
	}
	return db, goatcounter.NewContext(db), nil
}

func getVersion() string {
	return fmt.Sprintf("version=%s; go=%s; GOOS=%s; GOARCH=%s; race=%t; cgo=%t",
		goatcounter.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH,
		zruntime.Race, zruntime.CGO)
}
