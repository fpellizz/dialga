IF EXISTS (SELECT 1 FROM sys.databases WHERE name = N'golden_db' AND user_access_desc = N'SINGLE_USER')
    ALTER DATABASE [golden_db] SET MULTI_USER;
