package blob_store_test

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/ghttp"

	config_package "github.com/cloudfoundry-incubator/lattice/ltc/config"
	"github.com/cloudfoundry-incubator/lattice/ltc/config/blob_store"
	"github.com/cloudfoundry-incubator/lattice/ltc/config/persister"
	"github.com/goamz/goamz/aws"
	"github.com/goamz/goamz/s3"
)

var _ = Describe("BlobStore", func() {
	var (
		config     *config_package.Config
		httpClient *http.Client
		blobStore  blob_store.BlobStore
		fakeServer *ghttp.Server
		awsRegion  = aws.Region{Name: "riak-region-1", S3Endpoint: "http://s3.amazonaws.com"}
	)

	BeforeEach(func() {
		fakeServer = ghttp.NewServer()
		config = config_package.New(persister.NewMemPersister())

		fakeServerURL, err := url.Parse(fakeServer.URL())
		Expect(err).NotTo(HaveOccurred())
		proxyHostArr := strings.Split(fakeServerURL.Host, ":")
		Expect(proxyHostArr).To(HaveLen(2))
		proxyHostPort, err := strconv.Atoi(proxyHostArr[1])
		Expect(err).NotTo(HaveOccurred())
		config.SetBlobTarget(proxyHostArr[0], uint16(proxyHostPort), "V8GDQFR_VDOGM55IV8OH", "Wv_kltnl98hNWNdNwyQPYnFhK4gVPTxVS3NNMg==", "buck")

		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: config.BlobTarget().Proxy(),
				Dial: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).Dial,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		}

		s3Auth := aws.Auth{
			AccessKey: config.BlobTarget().AccessKey,
			SecretKey: config.BlobTarget().SecretKey,
		}

		s3S3 := s3.New(s3Auth, awsRegion, httpClient)
		s3S3.AttemptStrategy = aws.AttemptStrategy{}
		blobStore = blob_store.NewBlobStore(config, s3S3)
	})

	AfterEach(func() {
		fakeServer.Close()
	})

	Describe("BlobStore", func() {
		Describe("Bucket", func() {
			It("returns a bucket", func() {
				bucket := blobStore.Bucket("the-bucket-name")
				_, ok := bucket.(blob_store.BlobBucket)
				Expect(ok).To(BeTrue())
			})
		})
	})

	Describe("BlobBucket", func() {
		var blobBucket blob_store.BlobBucket

		BeforeEach(func() {
			blobBucket = blobStore.Bucket("the-bucket-name")
		})

		Describe("List", func() {
			It("lists objects in a bucket", func() {
				responseBody := `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>x</ID><DisplayName>x</DisplayName></Owner><Buckets><Bucket><Name>the-bucket-name</Name><CreationDate>2015-06-11T16:50:43.000Z</CreationDate></Bucket></Buckets></ListAllMyBucketsResult>`

				fakeServer.AppendHandlers(ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/the-bucket-name/", "delimiter=%2F&marker=&max-keys=1&prefix="),
					ghttp.RespondWith(http.StatusOK, responseBody, http.Header{"Content-Type": []string{"application/xml"}}),
				))

				_, err := blobBucket.List("", "/", "", 1)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeServer.ReceivedRequests()).To(HaveLen(1))
			})
		})
		Describe("PutReader", func() {
			It("puts an object into the bucket from a reader", func() {
				fakeServer.AppendHandlers(ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/the-bucket-name/object-key"),
					ghttp.VerifyHeader(http.Header{"X-Amz-Acl": []string{"public-read-write"}}),
					ghttp.RespondWith(http.StatusOK, "", http.Header{}),
				))

				bucketBuffer := gbytes.NewBuffer()
				n, err := bucketBuffer.Write([]byte("sample/data"))
				Expect(err).NotTo(HaveOccurred())
				blobBucket.PutReader("object-key", bucketBuffer, int64(n), "text/plain", s3.PublicReadWrite, s3.Options{})

				Expect(fakeServer.ReceivedRequests()).To(HaveLen(1))
			})
		})

	})
})
