<!-- XSLT 3.0: for-each-group group-by into 5,000 groups, an xsl:map per group, JSON output -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="json" indent="no"/>
  <xsl:template match="/">
    <xsl:map>
      <xsl:for-each-group select="/orders/order" group-by="customer">
        <xsl:map-entry key="current-grouping-key()">
          <xsl:map>
            <xsl:map-entry key="'count'" select="count(current-group())"/>
            <xsl:map-entry key="'total'" select="sum(current-group()/xs:decimal(price))"/>
            <xsl:map-entry key="'first'" select="string(current-group()[1]/@id)"/>
          </xsl:map>
        </xsl:map-entry>
      </xsl:for-each-group>
    </xsl:map>
  </xsl:template>
</xsl:stylesheet>
