package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/tencentyun/cos-go-sdk-v5"
	"github.com/ygpkg/yg-go/config"
	"github.com/ygpkg/yg-go/logs"
)

var _ iUploader = (*TencentCos)(nil)

// TencentCos .
type TencentCos struct {
	opt    config.StorageOption
	cosCfg config.TencentCOSConfig
	client *cos.Client
}

// NewTencentCos .
func NewTencentCos(cfg config.TencentCOSConfig, opt config.StorageOption) (*TencentCos, error) {
	ustr := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", cfg.Bucket, cfg.Region)
	u, err := url.Parse(ustr)
	if err != nil {
		logs.Errorf("parse url %s error: %v", ustr, err)
		return nil, err
	}
	b := &cos.BaseURL{BucketURL: u}
	c := cos.NewClient(b, &http.Client{
		//设置超时时间
		Transport: &cos.AuthorizationTransport{
			//如实填写账号和密钥，也可以设置为环境变量
			SecretID:  cfg.TencentConfig.SecretID,
			SecretKey: cfg.TencentConfig.SecretKey,
		},
	})

	return &TencentCos{
		cosCfg: cfg,
		opt:    opt,
		client: c,
	}, nil
}

// Save .
func (tc *TencentCos) Save(ctx context.Context, fi *FileInfo, r io.Reader) error {
	if fi.StoragePath == "" {
		return fmt.Errorf("storage path is empty")
	}
	if r == nil {
		return fmt.Errorf("reader is empty")
	}

	resp, err := tc.client.Object.Put(ctx, fi.StoragePath, r, nil)
	if err != nil {
		logs.Errorf("tencent cos put object error: %v", err)
		return err
	}
	defer resp.Body.Close()

	if md5str := resp.Header.Get("ETag"); md5str != "" {
		fi.Hash = "md5:" + strings.Trim(md5str, "\"")
	}
	fi.PublicURL = tc.GetPublicURL(fi.StoragePath, false)

	return nil
}

// GetPublicURL .
func (tc *TencentCos) GetPublicURL(storagePath string, temp bool) string {
	host := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", tc.cosCfg.Bucket, tc.cosCfg.Region)
	baseURI := host + storagePath
	if !temp {
		return baseURI
	}

	u, _ := url.Parse(host)
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		//设置超时时间
		Transport: &cos.AuthorizationTransport{
			//如实填写账号和密钥，也可以设置为环境变量
			SecretID:  tc.cosCfg.TencentConfig.SecretID,
			SecretKey: tc.cosCfg.TencentConfig.SecretKey,
			Expire:    tc.opt.PresignedTimeout,
		},
	})
	ctx := context.Background()
	presignedURL, err := client.Object.GetPresignedURL(ctx, http.MethodGet, storagePath,
		tc.cosCfg.TencentConfig.SecretID, tc.cosCfg.TencentConfig.SecretKey, tc.opt.PresignedTimeout, nil)
	if err != nil {
		logs.Errorf("tencent cos get presigned url error: %v", err)
		return ""
	}
	return presignedURL.String()
}

// GetPresignedURL 获取预签名URL
func (tc *TencentCos) GetPresignedURL(storagePath string) (string, error) {
	host := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", tc.cosCfg.Bucket, tc.cosCfg.Region)
	u, _ := url.Parse(host)
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		//设置超时时间
		Transport: &cos.AuthorizationTransport{
			//如实填写账号和密钥，也可以设置为环境变量
			SecretID:  tc.cosCfg.TencentConfig.SecretID,
			SecretKey: tc.cosCfg.TencentConfig.SecretKey,
			Expire:    tc.opt.PresignedTimeout,
		},
	})
	ctx := context.Background()
	presignedURL, err := client.Object.GetPresignedURL(ctx, http.MethodGet, storagePath,
		tc.cosCfg.TencentConfig.SecretID, tc.cosCfg.TencentConfig.SecretKey, tc.opt.PresignedTimeout, nil)
	if err != nil {
		logs.Errorf("tencent cos get presigned url error: %v", err)
		return "", err
	}
	return presignedURL.String(), nil
}

// InitUploadTask .
func (tc *TencentCos) InitUploadTask(ctx context.Context, tempFile *TempFile) error {
	initOpt := &cos.InitiateMultipartUploadOptions{
		ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{
			XCosMetaXXX: &http.Header{
				"chunk_hash": []string{tempFile.ChunkHash},
			},
		},
	}
	initRst, _, err := tc.client.Object.InitiateMultipartUpload(ctx, tempFile.StoragePath, initOpt)
	if err != nil {
		logs.Errorf("tencent cos initiate multipart upload error: %v", err)
		return err
	}
	tempFile.ThirdUploadID = initRst.UploadID

	return nil
}

// ListUploadExistsTrunk .
func (tc *TencentCos) ListUploadExistsTrunk(ctx context.Context, tempFile *TempFile) ([]int, error) {
	listRst, _, err := tc.client.Object.ListParts(ctx, tempFile.StoragePath, tempFile.ThirdUploadID, nil)
	if err != nil {
		logs.Errorf("tencent cos list parts error: %v", err)
		return nil, err
	}
	allPartNumber := make(map[int]struct{})
	for i := 1; i <= int(tempFile.PartCount); i++ {
		allPartNumber[i] = struct{}{}
	}

	exiPartNumber := make([]int, 0, len(listRst.Parts))
	for _, part := range listRst.Parts {
		thisPartNumber := part.PartNumber - 1 // cos partNumber 从1开始, 本地从0开始
		exiPartNumber = append(exiPartNumber, thisPartNumber)
	}
	sort.Ints(exiPartNumber)

	return exiPartNumber, nil
}

// UploadTrunk .
func (tc *TencentCos) UploadTrunk(ctx context.Context, tempFile *TempFile, partNumber int, r io.Reader, size int64) error {
	upOpt := &cos.ObjectUploadPartOptions{
		ContentLength: size,
	}
	_, err := tc.client.Object.UploadPart(ctx, tempFile.StoragePath, tempFile.ThirdUploadID, partNumber+1, r, upOpt)
	if err != nil {
		logs.Errorf("tencent cos upload part error: %v", err)
		return err
	}
	return nil
}

// CompleteUploadTask .
func (tc *TencentCos) CompleteUploadTask(ctx context.Context, tempFile *TempFile) error {
	var (
		filename = tempFile.StoragePath
		uploadid = tempFile.ThirdUploadID
	)

	listRst, _, err := tc.client.Object.ListParts(ctx, filename, uploadid, nil)
	if err != nil {
		logs.Errorf("tencent cos list parts error: %v", err)
		return err
	}
	compOpt := &cos.CompleteMultipartUploadOptions{
		Parts: listRst.Parts,
	}
	compRst, _, err := tc.client.Object.CompleteMultipartUpload(ctx, filename, uploadid, compOpt)
	if err != nil {
		logs.Errorf("tencent cos complete multipart upload error: %v", err)
		return err
	}

	logs.Infof("tencent cos complete multipart upload result: %v", compRst)
	return nil
}