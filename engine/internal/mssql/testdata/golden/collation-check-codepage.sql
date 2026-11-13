SELECT TOP (1) 1 FROM [golden_db].[dbo].[customers] WITH (READUNCOMMITTED) WHERE [city] IS NOT NULL AND (
  CAST(CAST(CAST([city] AS nvarchar(max)) COLLATE Cyrillic_General_CI_AS AS varchar(max)) AS nvarchar(max)) COLLATE Latin1_General_BIN2
    <> CAST([city] AS nvarchar(max)) COLLATE Latin1_General_BIN2
  OR DATALENGTH(CAST(CAST([city] AS nvarchar(max)) COLLATE Cyrillic_General_CI_AS AS varchar(max))) > 40);
