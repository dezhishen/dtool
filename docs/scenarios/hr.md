# 场景：HR / 人事

> 你手上是员工花名册（`hr.xlsx` 的 `员工` 表，60 行：工号、姓名、部门、职级、月薪、入职日期、绩效、邮箱）。
> 目标：做薪酬结构分析、找出待评估人员、给出一份部门人才盘点。

## 准备

```bash
mkdir -p /tmp/demo && cd /tmp/demo
cp /path/to/dtool/examples/data/hr.xlsx .
dtool convert --input hr.xlsx --sheet 员工 --name hr
dtool datasets show hr
```

`datasets show` 会列出每列的类型与取值分布（部门 6 个、职级 P3–P7、绩效 A/B/C），
先看它再写 SQL，能省掉「列名到底叫什么」的来回试错。

## ★ 各部门人数与薪酬

```bash
dtool query --sql "SELECT 部门, COUNT(*) AS 人数, ROUND(AVG(月薪),0) AS 平均月薪, MAX(月薪) AS 最高月薪
  FROM hr GROUP BY 1 ORDER BY 平均月薪 DESC"
```

```json
{"部门":"技术部","人数":14,"平均月薪":26721,"最高月薪":45800}
{"部门":"销售部","人数":15,"平均月薪":24467,"最高月薪":43500}
{"部门":"财务部","人数":9,"平均月薪":24289,"最高月薪":44100}
```

## ★ 工号是文本，前导零不会丢

```bash
dtool query --sql "SELECT 工号, 姓名, 部门 FROM hr WHERE 工号 BETWEEN '00101' AND '00105' ORDER BY 工号"
```

`00101` 这类值在导入时被整列识别为**字符串**，不会被压成 `101`，所以按字典序比较、按前缀筛选都正常。

## ★★ 找待评估人员（绩效为空 = 当年新人）

```bash
dtool query --format xlsx --output 待评估名单.xlsx --sql "SELECT 工号, 姓名, 部门, 入职日期
  FROM hr WHERE 绩效 IS NULL ORDER BY 入职日期"
```

绩效列在 2026 年入职者上是空的。空值必须用 `IS NULL` 判断，`= NULL` 永远不成立。

## ★★ 职级分布饼图

```bash
dtool query --sql "SELECT 职级, COUNT(*) AS 人数 FROM hr GROUP BY 1 ORDER BY 职级"
dtool visualize --input latest:query --type pie --x 职级 --y 人数 \
  --title "职级分布" --output 职级分布.png
```

```json
{"职级":"P3","人数":8} … {"职级":"P7","人数":11}
```

## ★★★ 部门 × 职级 交叉表（透视）

```bash
dtool query --format markdown --sql "SELECT 部门,
    SUM(CASE WHEN 职级 IN ('P6','P7') THEN 1 ELSE 0 END) AS 高级,
    SUM(CASE WHEN 职级 IN ('P4','P5') THEN 1 ELSE 0 END) AS 中级,
    SUM(CASE WHEN 职级 = 'P3' THEN 1 ELSE 0 END) AS 初级
  FROM hr GROUP BY 1 ORDER BY 高级 DESC"
```

用 `CASE WHEN` + `SUM` 把「行」转成「列」，是 SQL 里做透视表的通用做法，行数固定时很实用。

## ★★★ 各部门薪酬 TOP2（窗口函数）

```bash
dtool query --sql "SELECT 部门, 姓名, 月薪 FROM (
    SELECT 部门, 姓名, 月薪,
           RANK() OVER (PARTITION BY 部门 ORDER BY 月薪 DESC) AS 部门内排名
    FROM hr) WHERE 部门内排名 <= 2 ORDER BY 部门, 部门内排名"
```

```json
{"部门":"技术部","姓名":"陈洋","月薪":45800}
{"部门":"技术部","姓名":"王霞","月薪":45700}
{"部门":"销售部","姓名":"黄杰","月薪":43500}
```

`ROW_NUMBER` / `RANK` / `LAG` 等窗口函数均可使用，做组内排名、同比环比很省事。

## 常见坑

| 现象 | 处理 |
|------|------|
| 想按数值筛工号（`工号 > 100`） | 工号是文本列，用字符串比较：`工号 > '00100'` |
| 空值行查不出来 | 用 `IS NULL` / `IS NOT NULL` |
| 列名含特殊字符或重名 | 用双引号：`"名称_2"`、`"邮箱"` |
| 平均薪金有小数 | `ROUND(AVG(月薪),0)`，或直接取整数部分 |

相关：[SQL 规则](../../skills.md) · [场景总览](README.md)
