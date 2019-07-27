package self_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"sync"
	"time"

	quic "github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/integrationtests/tools/testserver"
	"github.com/lucas-clemente/quic-go/internal/protocol"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Bidirectional streams", func() {
	const numStreams = 300

	var (
		server     quic.Listener
		serverAddr string
		qconf      *quic.Config
	)

	for _, v := range []protocol.VersionNumber{protocol.VersionTLS} {
		version := v

		Context(fmt.Sprintf("with QUIC %s", version), func() {
			BeforeEach(func() {
				var err error
				qconf = &quic.Config{
					Versions:           []protocol.VersionNumber{version},
					MaxIncomingStreams: 0,
				}
				server, err = quic.ListenAddr("localhost:0", getTLSConfig(), qconf)
				Expect(err).ToNot(HaveOccurred())
				serverAddr = fmt.Sprintf("localhost:%d", server.Addr().(*net.UDPAddr).Port)
			})

			AfterEach(func() {
				server.Close()
			})

			FIt("test", func() {
				done := make(chan struct{}, 4)
				go func() {
					defer GinkgoRecover()
					sess, err := server.Accept(context.Background())
					Expect(err).ToNot(HaveOccurred())
					str, err := sess.AcceptStream(context.Background())
					Expect(err).ToNot(HaveOccurred())

					go func() {
						defer GinkgoRecover()
						fmt.Println("server writing data")
						defer fmt.Println("server done writing data")
						_, err := str.Write(testserver.PRDataLong)
						Expect(err).ToNot(HaveOccurred())
						Expect(str.Close()).To(Succeed())
						done <- struct{}{}
					}()

					go func() {
						defer GinkgoRecover()
						fmt.Println("server reading data")
						defer fmt.Println("server done reading data")
						data, err := ioutil.ReadAll(str)
						Expect(err).ToNot(HaveOccurred())
						Expect(data).To(Equal(testserver.PRDataLong))
						done <- struct{}{}
					}()
				}()

				sess, err := quic.DialAddr(
					serverAddr,
					getTLSClientConfig(),
					qconf,
				)
				Expect(err).ToNot(HaveOccurred())
				str, err := sess.OpenStream()
				Expect(err).ToNot(HaveOccurred())

				go func() {
					defer GinkgoRecover()
					fmt.Println("client writing data")
					defer fmt.Println("client done writing data")
					_, err := str.Write(testserver.PRDataLong)
					Expect(err).ToNot(HaveOccurred())
					Expect(str.Close()).To(Succeed())
					done <- struct{}{}
				}()

				go func() {
					defer GinkgoRecover()
					fmt.Println("client reading data")
					defer fmt.Println("client done reading data")
					data, err := ioutil.ReadAll(str)
					Expect(err).ToNot(HaveOccurred())
					Expect(data).To(Equal(testserver.PRDataLong))
					done <- struct{}{}
				}()

				Eventually(done, 20*time.Second).Should(HaveLen(4))
			})

			runSendingPeer := func(sess quic.Session) {
				var wg sync.WaitGroup
				wg.Add(numStreams)
				for i := 0; i < numStreams; i++ {
					str, err := sess.OpenStreamSync(context.Background())
					Expect(err).ToNot(HaveOccurred())
					data := testserver.GeneratePRData(25 * i)
					go func() {
						defer GinkgoRecover()
						_, err := str.Write(data)
						Expect(err).ToNot(HaveOccurred())
						Expect(str.Close()).To(Succeed())
					}()
					go func() {
						defer GinkgoRecover()
						defer wg.Done()
						dataRead, err := ioutil.ReadAll(str)
						Expect(err).ToNot(HaveOccurred())
						Expect(dataRead).To(Equal(data))
					}()
				}
				wg.Wait()
			}

			runReceivingPeer := func(sess quic.Session) {
				var wg sync.WaitGroup
				wg.Add(numStreams)
				for i := 0; i < numStreams; i++ {
					str, err := sess.AcceptStream(context.Background())
					Expect(err).ToNot(HaveOccurred())
					go func() {
						defer GinkgoRecover()
						defer wg.Done()
						// shouldn't use io.Copy here
						// we should read from the stream as early as possible, to free flow control credit
						data, err := ioutil.ReadAll(str)
						Expect(err).ToNot(HaveOccurred())
						_, err = str.Write(data)
						Expect(err).ToNot(HaveOccurred())
						Expect(str.Close()).To(Succeed())
					}()
				}
				wg.Wait()
			}

			It(fmt.Sprintf("client opening %d streams to a server", numStreams), func() {
				var sess quic.Session
				go func() {
					defer GinkgoRecover()
					var err error
					sess, err = server.Accept(context.Background())
					Expect(err).ToNot(HaveOccurred())
					runReceivingPeer(sess)
				}()

				client, err := quic.DialAddr(
					serverAddr,
					getTLSClientConfig(),
					qconf,
				)
				Expect(err).ToNot(HaveOccurred())
				runSendingPeer(client)
			})

			It(fmt.Sprintf("server opening %d streams to a client", numStreams), func() {
				go func() {
					defer GinkgoRecover()
					sess, err := server.Accept(context.Background())
					Expect(err).ToNot(HaveOccurred())
					runSendingPeer(sess)
					sess.Close()
				}()

				client, err := quic.DialAddr(
					serverAddr,
					getTLSClientConfig(),
					qconf,
				)
				Expect(err).ToNot(HaveOccurred())
				runReceivingPeer(client)
				Eventually(client.Context().Done()).Should(BeClosed())
			})

			It(fmt.Sprintf("client and server opening %d each and sending data to the peer", numStreams), func() {
				done1 := make(chan struct{})
				go func() {
					defer GinkgoRecover()
					sess, err := server.Accept(context.Background())
					Expect(err).ToNot(HaveOccurred())
					done := make(chan struct{})
					go func() {
						defer GinkgoRecover()
						runReceivingPeer(sess)
						close(done)
					}()
					runSendingPeer(sess)
					<-done
					close(done1)
				}()

				client, err := quic.DialAddr(
					serverAddr,
					getTLSClientConfig(),
					qconf,
				)
				Expect(err).ToNot(HaveOccurred())
				done2 := make(chan struct{})
				go func() {
					defer GinkgoRecover()
					runSendingPeer(client)
					close(done2)
				}()
				runReceivingPeer(client)
				<-done1
				<-done2
			})
		})
	}
})
