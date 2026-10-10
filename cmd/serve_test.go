package cmd

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/0xERR0R/blocky/helpertest"
	"github.com/0xERR0R/blocky/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

const (
	basePort = 5000
)

var _ = Describe("Serve command", func() {
	var (
		tmpDir *helpertest.TmpFolder
		port   string
	)
	BeforeEach(func() {
		port = helpertest.GetStringPort(basePort)
		tmpDir = helpertest.NewTmpFolder("config")

		configPath = defaultConfigPath
	})

	When("Serve command is called with valid config", func() {
		It("should start without error and terminate with signal", func() {
			By("initialize config", func() {
				cfgFile := tmpDir.CreateStringFile("config.yaml",
					"upstreams:",
					"  groups:",
					"    default:",
					"      - 1.1.1.1",
					"ports:",
					"  dns: "+port)

				os.Setenv(configFileEnvVar, cfgFile.Path)
				DeferCleanup(func() { os.Unsetenv(configFileEnvVar) })

				Expect(initConfig()).Should(Succeed())
			})

			errChan := make(chan error)
			By("start server", func() {
				go func() {
					// it is a blocking function, call async
					errChan <- startServer(newServeCommand(), []string{})
				}()
			})

			By("check DNS port is open", func() {
				Eventually(func(g Gomega) {
					conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
					g.Expect(err).Should(Succeed())
					defer conn.Close()
				}, "5s").Should(Succeed())
			})

			By("terminate with signal", func() {
				signals <- syscall.SIGINT

				// no errors
				Eventually(errChan).Should(Receive(BeNil()))
			})
		})
	})

	When("Serve command is called with valid config", func() {
		It("should fail if server start fails", func() {
			By("start http server on port "+port, func() {
				go func(p string) {
					Expect(http.ListenAndServe(":"+p, nil)).Should(Succeed())
				}(port)
			})
			By("initialize config with blocked port "+port, func() {
				cfgFile := tmpDir.CreateStringFile("config.yaml",
					"upstreams:",
					"  groups:",
					"    default:",
					"      - 1.1.1.1",
					"ports:",
					"  dns: "+port)

				os.Setenv(configFileEnvVar, cfgFile.Path)
				DeferCleanup(func() { os.Unsetenv(configFileEnvVar) })

				Expect(initConfig()).Should(Succeed())
			})

			errChan := make(chan error)
			By("start server", func() {
				go func() {
					// it is a blocking function, call async
					errChan <- startServer(newServeCommand(), []string{})
				}()
			})

			By("terminate with signal", func() {
				var startError error
				Eventually(errChan, "10s").Should(Receive(&startError))
				Expect(startError).Should(MatchError(ContainSubstring("address already in use")))
			})
		})
	})

	When("Serve command is called with a config that logs diagnostics", func() {
		// runServer starts the server with the config files given, and stops it
		runServer := func(files ...[]string) *test.Hook {
			for i, lines := range files {
				tmpDir.CreateStringFile(fmt.Sprintf("config%d.yml", i), lines...)
			}

			os.Setenv(configFileEnvVar, tmpDir.Path)
			DeferCleanup(func() { os.Unsetenv(configFileEnvVar) })
			// the server's goroutines may still log, so keep the logger and mute it
			DeferCleanup(func() { log.Log().SetOutput(io.Discard) })

			hook := captureLog()

			cmd := newServeCommand()
			Expect(cmd.PersistentPreRunE(cmd, []string{})).Should(Succeed())

			errChan := make(chan error)
			go func() {
				errChan <- startServer(cmd, []string{})
			}()

			Eventually(func(g Gomega) {
				conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
				g.Expect(err).Should(Succeed())
				defer conn.Close()
			}, "5s").Should(Succeed())

			signals <- syscall.SIGINT
			Eventually(errChan).Should(Receive(BeNil()))

			return hook
		}

		count := func(hook *test.Hook, substr string) int {
			n := 0

			for _, entry := range hook.AllEntries() {
				if strings.Contains(entry.Message, substr) {
					n++
				}
			}

			return n
		}

		It("should log them once, where and at the level the config says", func() {
			hook := runServer(
				[]string{
					"upstream:",
					"  default:",
					"    - 1.1.1.1",
					"caching:",
					"  minTime: 5",
					"ports:",
					"  dns: " + port,
				},
				[]string{
					"log:",
					"  level: warn",
					"  target: syslog",
					"  syslog:",
					"    network: tcp",
					"    address: 127.0.0.1:1",
				})

			// both log at info, so they show only if the level was set too late
			Expect(count(hook, "loading config files")).Should(BeZero())
			Expect(count(hook, "_/_/")).Should(BeZero())

			Expect(count(hook, "configuration uses deprecated options")).Should(Equal(1))
			Expect(count(hook, "duration without a unit")).Should(Equal(1))
			Expect(count(hook, "can't log to syslog")).Should(Equal(1))
		})

		It("should log at the level a deprecated option sets", func() {
			hook := runServer([]string{
				"upstreams:",
				"  groups:",
				"    default:",
				"      - 1.1.1.1",
				"ports:",
				"  dns: " + port,
				"logLevel: warn",
				"log:",
				"  target: syslog",
				"  syslog:",
				"    network: tcp",
				"    address: 127.0.0.1:1",
			})

			Expect(count(hook, "_/_/")).Should(BeZero())
			Expect(count(hook, "configuration uses deprecated options")).Should(Equal(1))
			Expect(count(hook, "can't log to syslog")).Should(Equal(1))
		})
	})

	When("Serve command is called with a listen address the commands can't parse", func() {
		DescribeTable("should fail to start and report it",
			func(listen, expected string) {
				cfgFile := tmpDir.CreateStringFile("config.yaml",
					"upstreams:",
					"  groups:",
					"    default:",
					"      - 1.1.1.1",
					"ports:",
					listen)

				os.Setenv(configFileEnvVar, cfgFile.Path)
				DeferCleanup(func() { os.Unsetenv(configFileEnvVar) })
				DeferCleanup(func() { log.Log().SetOutput(io.Discard) })

				cmd := newServeCommand()
				Expect(cmd.PersistentPreRunE(cmd, []string{})).Should(Succeed())
				Expect(startServer(cmd, []string{})).Should(MatchError(ContainSubstring(expected)))
			},
			Entry("DNS", "  dns: 1.2.3.4:99999", "can't parse DNS listen address"),
			Entry("HTTP", "  http: abc", "can't convert port 'abc'"),
		)
	})

	When("Serve command is called without config", func() {
		It("should fail to start and report error", func() {
			errChan := make(chan error)
			By("start server", func() {
				go func() {
					// it is a blocking function, call async
					errChan <- startServer(newServeCommand(), []string{})
				}()
			})

			By("server should terminate with error", func() {
				var startError error
				Eventually(errChan).Should(Receive(&startError))
				Expect(startError).Should(MatchError(ContainSubstring("unable to load configuration")))
			})
		})
	})
})

// captureLog records what the global logger logs until the spec ends
func captureLog() *test.Hook {
	oldHooks := make(logrus.LevelHooks)
	for level, hooks := range log.Log().Hooks {
		oldHooks[level] = append(oldHooks[level], hooks...)
	}

	hook := test.NewLocal(log.Log())
	DeferCleanup(func() { log.Log().ReplaceHooks(oldHooks) })

	return hook
}
