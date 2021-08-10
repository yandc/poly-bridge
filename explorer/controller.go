/*
 * Copyright (C) 2020 The poly network Authors
 * This file is part of The poly network library.
 *
 * The  poly network  is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Lesser General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * The  poly network  is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Lesser General Public License for more details.
 * You should have received a copy of the GNU Lesser General Public License
 * along with The poly network .  If not, see <http://www.gnu.org/licenses/>.
 */

package explorer

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/beego/beego/v2/core/logs"
	"github.com/beego/beego/v2/server/web"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"poly-bridge/basedef"
	"poly-bridge/conf"
	"poly-bridge/models"
	"strconv"
)

var db *gorm.DB
var redis *RedisCache

func Init() {
	dbConfig := conf.GlobalConfig.DBConfig
	Logger := logger.Default
	if conf.GlobalConfig.RunMode == "dev" {
		Logger = Logger.LogMode(logger.Info)
	}
	dbConn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8", dbConfig.User, dbConfig.Password, dbConfig.URL, dbConfig.Scheme)
	var err error
	db, err = gorm.Open(mysql.Open(dbConn), &gorm.Config{Logger: Logger})
	if err != nil {
		panic(err)
	}

	redisConfig := conf.GlobalConfig.RedisConfig
	redis, err = GetRedisClient(redisConfig)
	if err != nil {
		logs.Error("GetRedisClient redis err")
	}

	// Preload chains info
	chains := []*models.Chain{}
	err = db.Find(&chains).Error
	if err != nil {
		panic(err)
	}
	models.Init(chains)
}

type ExplorerController struct {
	web.Controller
}

// GetExplorerInfo shows explorer information, such as current blockheight (the number of blockchain and so on) on the home page.
func (c *ExplorerController) GetExplorerInfo() {

	//get all chains
	chains := make([]*models.Chain, 0)
	res := db.Find(&chains)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chain does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}

	// get all chains statistic
	chainStatistics := make([]*models.ChainStatistic, 0)
	if db.Find(&chainStatistics).Error != nil {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chain stats does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}

	// get all tokens
	tokenBasics := make([]*models.TokenBasic, 0)
	res = db.Where("property = ?", 1).
		Preload("Tokens").Find(&tokenBasics)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chain does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}

	c.Data["json"] = models.MakeExplorerInfoResp(chains, chainStatistics, tokenBasics)
	c.ServeJSON()
}

func (c *ExplorerController) GetTokenTxList() {
	// get parameter
	var tokenTxListReq models.TokenTxListReq
	var err error
	if err = json.Unmarshal(c.Ctx.Input.RequestBody, &tokenTxListReq); err != nil {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("request parameter is invalid!"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
	}
	transactionOnTokens := make([]*models.TransactionOnToken, 0)
	res := db.Raw(`select a.hash, a.height, a.time, a.chain_id, b.from, b.to, b.amount, 1 as direct from src_transactions a inner join src_transfers b on a.hash = b.tx_hash where b.chain_id = ? and b.asset = ?
		union select c.hash, c.height, c.time, c.chain_id, d.from, d.to, d.amount, 2 as direct from dst_transactions c inner join dst_transfers d on c.hash = d.tx_hash where d.chain_id = ? and d.asset = ?
		order by height desc limit ?,?`,
		tokenTxListReq.ChainId, tokenTxListReq.Token, tokenTxListReq.ChainId, tokenTxListReq.Token, (tokenTxListReq.PageNo-1)*tokenTxListReq.PageSize, tokenTxListReq.PageSize).
		Scan(&transactionOnTokens)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("transactionOnTokens does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	counter := struct {
		Counter int64
	}{}
	res = db.Raw("select sum(in_counter)+sum(out_counter) as counter from token_statistics where chain_id = ? and hash = ?", tokenTxListReq.ChainId, tokenTxListReq.Token).
		Scan(&counter)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("tokenStatistic does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	token := &models.Token{}
	db.Where("chain_id=? and hash=? and property = ?", tokenTxListReq.ChainId, tokenTxListReq.Token, 1).
		Preload("TokenBasic").
		First(token)
	c.Data["json"] = models.MakeTokenTxList(transactionOnTokens, counter.Counter, token)
	c.ServeJSON()
}

func (c *ExplorerController) GetAddressTxList() {
	// get parameter
	var addressTxListReq models.AddressTxListReq
	var err error
	if err = json.Unmarshal(c.Ctx.Input.RequestBody, &addressTxListReq); err != nil {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("request parameter is invalid!"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
	}
	addressTxListReq.Address, _ = basedef.Address2Hash(addressTxListReq.ChainId, addressTxListReq.Address)
	transactionOnAddresses := make([]*models.TransactionOnAddress, 0)
	res := db.Debug().Raw(`select a.hash, a.height, a.time, a.chain_id, b.from, b.to, b.amount, c.hash as token_hash, c.token_type, c.name as token_name, 1 as direct, m.precision from src_transactions a left join src_transfers b on a.hash = b.tx_hash left join tokens c on b.asset = c.hash and b.chain_id = c.chain_id left JOIN token_basics m on c.token_basic_name = m.name where b.from = ? and b.chain_id = ? 
		union select d.hash, d.height, d.time, d.chain_id, e.from, e.to, e.amount, f.hash as token_hash, f.token_type, f.name as token_name, 2,n.precision as direct from dst_transactions d left join dst_transfers e on d.hash = e.tx_hash left join tokens f on e.asset = f.hash and e.chain_id = f.chain_id left JOIN token_basics n on f.token_basic_name = n.name where e.to = ? and e.chain_id = ? 
		order by height desc limit ?,?`,
		addressTxListReq.Address, addressTxListReq.ChainId, addressTxListReq.Address, addressTxListReq.ChainId, (addressTxListReq.PageNo-1)*addressTxListReq.PageSize, addressTxListReq.PageSize).
		Find(&transactionOnAddresses)
	if errors.Is(res.Error, gorm.ErrRecordNotFound) {
		c.Data["json"] = &models.AddressTxListResp{
			Total: 0,
		}
		c.Ctx.ResponseWriter.WriteHeader(200)
		c.ServeJSON()
		return
	}
	if res.Error != nil {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("transactionOnAddresses does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}

	counter := struct {
		Counter int64
	}{}
	res = db.Raw(`select sum(cnt) as counter from (select count(*) as cnt from src_transactions a left join src_transfers b on a.hash = b.tx_hash left join tokens c on b.asset = c.hash and b.chain_id = c.chain_id where b.from = ? and b.chain_id = ? 
		union select count(*) as cnt from dst_transactions d left join dst_transfers e on d.hash = e.tx_hash left join tokens f on e.asset = f.hash and e.chain_id = f.chain_id where e.to = ? and e.chain_id = ?) as u`,
		addressTxListReq.Address, addressTxListReq.ChainId, addressTxListReq.Address, addressTxListReq.ChainId).
		Find(&counter)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("counter does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	c.Data["json"] = models.MakeAddressTxList(transactionOnAddresses, counter.Counter)
	c.ServeJSON()
}

// TODO GetCrossTxList gets Cross transaction list from start to end (to be optimized)
func (c *ExplorerController) GetCrossTxList() {
	// get parameter
	var crossTxListReq models.CrossTxListReq
	var err error
	if err = json.Unmarshal(c.Ctx.Input.RequestBody, &crossTxListReq); err != nil {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("request parameter is invalid!"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
	}
	srcPolyDstRelations := make([]*models.SrcPolyDstRelation, 0)
	res := db.Debug().Model(&models.PolyTransaction{}).
		Select("src_transactions.hash as src_hash, poly_transactions.hash as poly_hash, dst_transactions.hash as dst_hash").
		Where("src_transactions.standard = ?", 0).
		Joins("left join src_transactions on src_transactions.hash = poly_transactions.src_hash").
		Joins("left join dst_transactions on poly_transactions.hash = dst_transactions.poly_hash").
		Order("poly_transactions.time desc").
		Limit(crossTxListReq.PageSize).Offset((crossTxListReq.PageNo - 1) * crossTxListReq.PageSize).
		Find(&srcPolyDstRelations)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("srcPolyDstRelations does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	for _, srcPolyDstRelation := range srcPolyDstRelations {
		srcPolyDstRelation.PolyTransaction = new(models.PolyTransaction)
		err = db.Where("hash=?", srcPolyDstRelation.PolyHash).First(srcPolyDstRelation.PolyTransaction).Error
		if err == nil {
			if srcPolyDstRelation.DstHash != "" {
				srcPolyDstRelation.PolyTransaction.State = 1
			} else {
				srcPolyDstRelation.PolyTransaction.State = 0
			}
		}
	}
	counter, err := redis.GetCrossTxCounter()
	if err != nil {
		logs.Info(err)
		res = db.Debug().Model(&models.PolyTransaction{}).
			Select("src_transactions.hash as src_hash, poly_transactions.hash as poly_hash, dst_transactions.hash as dst_hash").
			Where("src_transactions.standard = ?", 0).
			Joins("left join src_transactions on src_transactions.hash = poly_transactions.src_hash").
			Joins("left join dst_transactions on poly_transactions.hash = dst_transactions.poly_hash").
			Count(&counter)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("CrossTxCounter does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
		err = redis.SetCrossTxCounter(counter)
		if err != nil {
			logs.Error(err)
		}
	}

	c.Data["json"] = models.MakeCrossTxListResp(srcPolyDstRelations, counter)
	c.ServeJSON()
}

// GetCrossTx gets cross tx by Tx
func (c *ExplorerController) GetCrossTx() {
	var crossTxReq models.CrossTxReq
	if len(c.Ctx.Input.Query("txhash")) == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("request parameter is invalid!"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
	}
	crossTxReq.TxHash = c.Ctx.Input.Query("txhash")
	fmt.Println("crossTxReq.TxHash", crossTxReq.TxHash)
	relations := make([]*models.PolyTxRelation, 0)
	res := db.Model(&models.SrcTransaction{}).
		Select("src_transactions.hash as src_hash, poly_transactions.hash as poly_hash, dst_transactions.hash as dst_hash, src_transactions.chain_id as chain_id, src_transfers.asset as token_hash, src_transfers.dst_chain_id as to_chain_id, src_transfers.dst_asset as to_token_hash, dst_transfers.chain_id as dst_chain_id, dst_transfers.asset as dst_token_hash").
		Where("src_transactions.standard = ? and (src_transactions.hash = ? or poly_transactions.hash = ? or dst_transactions.hash = ?)", 0, crossTxReq.TxHash, crossTxReq.TxHash, crossTxReq.TxHash).
		Joins("left join src_transfers on src_transactions.hash = src_transfers.tx_hash").
		Joins("left join poly_transactions on src_transactions.hash = poly_transactions.src_hash").
		Joins("left join dst_transactions on poly_transactions.hash = dst_transactions.poly_hash").
		Joins("left join dst_transfers on dst_transfers.tx_hash = dst_transactions.hash").
		Find(&relations)
	if errors.Is(res.Error, gorm.ErrRecordNotFound) {
		c.Data["json"] = &models.AddressTxListResp{
			Total: 0,
		}
		c.Ctx.ResponseWriter.WriteHeader(200)
		c.ServeJSON()
		return
	}
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("relations does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	relation := relations[0]
	token := new(models.Token)
	err := db.Where("hash = ? and chain_id =?", relation.TokenHash, relation.ChainId).
		First(token).Error
	if err == nil {
		relation.Token = token
	}
	srcTransaction := new(models.SrcTransaction)
	err = db.Where("hash = ?", relation.SrcHash).
		Preload("SrcTransfer").
		First(srcTransaction).Error
	if err == nil {
		relation.SrcTransaction = srcTransaction
		if srcTransaction.SrcTransfer == nil {
			srcTransaction.SrcTransfer = new(models.SrcTransfer)
		}
	}
	polyTransaction := new(models.PolyTransaction)
	err = db.Where("hash=?", relation.PolyHash).First(polyTransaction).Error
	if err == nil {
		relation.PolyTransaction = polyTransaction
	}
	dstTransaction := new(models.DstTransaction)
	err = db.Where("hash=?", relation.DstHash).
		Preload("DstTransfer").
		First(dstTransaction).Error
	if err == nil {
		relation.DstTransaction = dstTransaction
		if dstTransaction.DstTransfer == nil {
			dstTransaction.DstTransfer = new(models.DstTransfer)
		}
	}
	toToken := new(models.Token)
	err = db.Where("hash = ? and chain_id =?", relation.ToTokenHash, relation.ToChainId).
		First(toToken).Error
	if err == nil {
		relation.ToToken = toToken
	}
	dstToken := new(models.Token)
	err = db.Where("hash = ? and chain_id =?", relation.DstTokenHash, relation.DstChainId).
		First(dstToken).Error
	if err == nil {
		relation.DstToken = dstToken
	}
	c.Data["json"] = models.MakeCrossTxResp(relation)
	c.ServeJSON()
}

func (c *ExplorerController) GetAssetStatistic() {
	assetStatistics := make([]*models.AssetStatistic, 0)
	res := db.Find(&assetStatistics)
	if res.RowsAffected == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("assetStatistic does not exist"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
		return
	}
	c.Data["json"] = models.MakeAssetInfoResp(assetStatistics)
	c.ServeJSON()
}

func (c *ExplorerController) GetTransferStatistic() {
	var transferStatisticReq models.TransferStatisticReq
	if len(c.Ctx.Input.Query("chain")) == 0 {
		c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("getTransferStatistic request parameter is invalid!"))
		c.Ctx.ResponseWriter.WriteHeader(400)
		c.ServeJSON()
	}
	if chainId, err := strconv.Atoi(c.Ctx.Input.Query("chain")); err == nil {
		transferStatisticReq.Chain = uint64(chainId)
	}
	req, _ := json.Marshal(transferStatisticReq)
	logs.Info("GetTransferStatistic transferStatisticReq" + string(req))

	tokenStatistics := make([]*models.TokenStatistic, 0)
	chainStatistics := make([]*models.ChainStatistic, 0)
	chains := make([]*models.Chain, 0)
	if transferStatisticReq.Chain == uint64(0) {
		res := db.Preload("Token").Preload("Token.TokenBasic").Find(&tokenStatistics)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("transferStatistics does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
		res = db.Model(&models.ChainStatistic{}).Find(&chainStatistics)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chainStatistics does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
		res = db.Model(&models.Chain{}).Find(&chains)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chains does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
	} else {
		res := db.
			Where("chain_id=?", transferStatisticReq.Chain).
			Preload("Token").Preload("Token.TokenBasic").
			Find(&tokenStatistics)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("transferStatistics does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
		res = db.Model(&models.ChainStatistic{}).
			Where("chain_id=?", transferStatisticReq.Chain).Find(&chainStatistics)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chainStatistics does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
		res = db.Model(&models.Chain{}).
			Where("chain_id=?", transferStatisticReq.Chain).Find(&chains)
		if res.RowsAffected == 0 {
			c.Data["json"] = models.MakeErrorRsp(fmt.Sprintf("chains does not exist"))
			c.Ctx.ResponseWriter.WriteHeader(400)
			c.ServeJSON()
			return
		}
	}
	c.Data["json"] = models.MakeTransferInfoResp(tokenStatistics, chainStatistics, chains)
	c.ServeJSON()
}