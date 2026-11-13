SELECT TOP (1) 1 FROM [golden_db].[dbo].[orders] WITH (READUNCOMMITTED) WHERE ([code] IS NOT NULL)
GROUP BY [code] COLLATE Latin1_General_100_CS_AS, [year] HAVING COUNT_BIG(*) > 1;
