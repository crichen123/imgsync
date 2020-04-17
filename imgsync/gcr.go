package imgsync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/parnurzeal/gorequest"

	jsoniter "github.com/json-iterator/go"
	"github.com/sirupsen/logrus"
)

type Gcr struct {
	NameSpace         string
	DockerHubUser     string
	DockerHubPassword string
	SyncTimeOut       time.Duration
	HttpTimeOut       time.Duration
	QueryLimit        int
	ProcessLimit      int
	queryLimitCh      chan int
	processLimitCh    chan int
}

// init gcr client
func (g *Gcr) Init() *Gcr {

	if g.NameSpace == "" {
		g.NameSpace = "google-containers"
	}

	if g.SyncTimeOut == 0 {
		g.SyncTimeOut = 1 * time.Hour
	}

	if g.HttpTimeOut == 0 {
		g.HttpTimeOut = 5 * time.Second
	}

	if g.QueryLimit == 0 {
		// query limit default 20
		g.queryLimitCh = make(chan int, 20)
	} else {
		g.queryLimitCh = make(chan int, g.QueryLimit)
	}

	if g.ProcessLimit == 0 {
		// process limit default 20
		g.processLimitCh = make(chan int, 20)
	} else {
		g.processLimitCh = make(chan int, g.ProcessLimit)
	}

	if g.DockerHubUser == "" || g.DockerHubPassword == "" {
		logrus.Fatal("docker hub user or password is empty")
	}

	logrus.Infoln("init success...")

	return g
}

func (g *Gcr) Sync() {

	logrus.Info("starting sync gcr images...")

	gcrImages := g.gcrImageList()
	logrus.Infof("Google container registry images total: %d", len(gcrImages))

	ctx, cancel := context.WithTimeout(context.Background(), g.SyncTimeOut)
	defer cancel()

	processWg := new(sync.WaitGroup)
	processWg.Add(len(gcrImages))

	for _, image := range gcrImages {
		tmpImage := image
		go func() {
			defer func() {
				<-g.processLimitCh
				processWg.Done()
			}()
			select {
			case g.processLimitCh <- 1:
				g.process(tmpImage)
			case <-ctx.Done():
			}
		}()
	}

	processWg.Wait()

}

func (g *Gcr) gcrImageList() []Image {

	publicImageNames := g.gcrPublicImageNames()

	logrus.Info("get gcr public image tags...")

	imgCh := make(chan Image, 20)
	imgGetWg := new(sync.WaitGroup)
	imgGetWg.Add(len(publicImageNames))

	for _, imageName := range publicImageNames {
		tmpImageName := imageName
		go func() {
			defer func() {
				<-g.queryLimitCh
				imgGetWg.Done()
			}()

			g.queryLimitCh <- 1

			logrus.Debugf("get gcr image %s/%s tags.", g.NameSpace, tmpImageName)
			resp, body, errs := gorequest.New().
				Timeout(g.HttpTimeOut).
				Retry(3, 1*time.Second).
				Get(fmt.Sprintf(GcrImageTagsTpl, g.NameSpace, tmpImageName)).
				EndBytes()
			if errs != nil {
				logrus.Errorf("failed to get gcr image tags, namespace: %s, image: %s, error: %s", g.NameSpace, tmpImageName, errs)
				return
			}
			defer func() { _ = resp.Body.Close() }()

			var tags []string
			err := jsoniter.UnmarshalFromString(jsoniter.Get(body, "tags").ToString(), &tags)
			if err != nil {
				logrus.Errorf("failed to get gcr image tags, namespace: %s, image: %s, error: %s", g.NameSpace, tmpImageName, err)
				return
			}

			for _, tag := range tags {
				imgCh <- Image{
					Repo: "gcr.io",
					User: g.NameSpace,
					Name: tmpImageName,
					Tag:  tag,
				}
			}

		}()
	}

	var images []Image
	go func() {
		for {
			select {
			case image, ok := <-imgCh:
				if ok {
					images = append(images, image)
				} else {
					break
				}
			}
		}
	}()

	imgGetWg.Wait()
	close(imgCh)
	return images
}

func (g *Gcr) gcrPublicImageNames() []string {

	logrus.Info("get gcr public images...")

	resp, body, errs := gorequest.New().
		Timeout(g.HttpTimeOut).
		Retry(3, 1*time.Second).
		Get(fmt.Sprintf(GcrImagesTpl, g.NameSpace)).
		EndBytes()
	if errs != nil {
		logrus.Fatalf("failed to get gcr images, namespace: %s, error: %s", g.NameSpace, errs)
	}
	defer func() { _ = resp.Body.Close() }()

	var imageNames []string
	err := jsoniter.UnmarshalFromString(jsoniter.Get(body, "child").ToString(), &imageNames)
	if err != nil {
		logrus.Fatalf("failed to get gcr images, namespace: %s, error: %s", g.NameSpace, err)
	}
	return imageNames
}

func (g *Gcr) process(image Image) {
	logrus.Infof("process image: %s", image)
	err := syncDockerHub(image, DockerHubOption{
		Username: g.DockerHubUser,
		Password: g.DockerHubPassword,
		Timeout:  10 * time.Minute,
	})
	if err != nil {
		logrus.Errorf("failed to process image %s, error: %s", image, err)
	}
}