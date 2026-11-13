SELECT TOP (1) 1 FROM [golden_db].[dbo].[lines] AS c WITH (READUNCOMMITTED)
WHERE c.[order_code] IS NOT NULL AND c.[year] IS NOT NULL AND NOT EXISTS (SELECT 1 FROM [golden_db].[dbo].[orders] AS p WITH (READUNCOMMITTED)
  WHERE p.[code] COLLATE Latin1_General_100_CS_AS = c.[order_code] COLLATE Latin1_General_100_CS_AS AND p.[year] = c.[year]);
