#!/bin/bash

# 清理数据库脚本

echo "警告：此操作将清空所有任务、商品和关联数据！"
read -p "确认继续？(yes/no): " confirm

if [ "$confirm" != "yes" ]; then
    echo "操作已取消"
    exit 0
fi

echo "正在清理数据库..."

docker exec -it goodshunter-mysql mysql -u root -pgoodshunter_root goodshunter -e "
    SET FOREIGN_KEY_CHECKS = 0;
    TRUNCATE TABLE task_items;
    TRUNCATE TABLE items;
    TRUNCATE TABLE tasks;
    SET FOREIGN_KEY_CHECKS = 1;
    ALTER TABLE tasks AUTO_INCREMENT = 1;
    ALTER TABLE items AUTO_INCREMENT = 1;
    ALTER TABLE task_items AUTO_INCREMENT = 1;
"

echo "数据库清理完成！"
echo "任务ID将从1重新开始。"
